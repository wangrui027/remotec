package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 常量定义
const (
	appVersion  = "1.0.0"
	timeFormat  = "2006-01-02 15:04:05"
	contentType = "application/json; charset=utf-8"
)

// 命令行参数
var (
	port     string
	command  string
	token    string
	endpoint string
	showHelp bool
)

// 执行管理结构
type Execution struct {
	ID      string
	Cancel  context.CancelFunc
	Stopped bool
}

// 全局变量
var (
	execLock   sync.Mutex
	executions = make(map[string]*Execution)
)

// 响应结构
type CommandResult struct {
	ExecID     string  `json:"exec_id"`
	Status     string  `json:"status"`
	Command    string  `json:"command"`
	Message    string  `json:"message"`
	ExecTime   string  `json:"exec_time"`
	ExecSecond float64 `json:"exec_second"`
	Output     string  `json:"output"`
}

func init() {
	flag.StringVar(&port, "p", "", "监听的端口号")
	flag.StringVar(&command, "c", "", "要执行的命令")
	flag.StringVar(&token, "token", "", "认证token")
	flag.StringVar(&endpoint, "endpoint", "", "自定义端点路径")
	flag.BoolVar(&showHelp, "help", false, "显示帮助信息")
	flag.BoolVar(&showHelp, "h", false, "")
}

func main() {
	flag.Parse()
	setupLogger()

	if showHelp || len(os.Args) == 1 {
		printHelp()
		return
	}

	if port == "" || command == "" {
		logError("必须提供端口号(-p)和命令(-c)")
		os.Exit(1)
	}

	startServer()
}

func startServer() {
	endpointPath := getEndpoint()
	url := fmt.Sprintf("http://localhost:%s/%s", port, endpointPath)
	logInfo("服务启动成功，监听端点：%s", url)

	handler := http.HandlerFunc(requestHandler)
	if token != "" {
		handler = authMiddleware(handler)
	}

	http.HandleFunc("/"+endpointPath, handler)
	logInfo("开始监听端口：%s", port)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		logError("服务器启动失败: %v", err)
		os.Exit(1)
	}
}

func requestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	action := r.URL.Query().Get("action")
	switch action {
	case "loop":
		handleLoop(w, r)
	case "multiple":
		handleMultiple(w, r)
	case "stop":
		handleStop(w, r)
	default:
		handleSingle(w, r)
	}
}

func handleLoop(w http.ResponseWriter, r *http.Request) {
	delay, _ := strconv.Atoi(r.URL.Query().Get("delay"))
	execID := generateID()
	ctx, cancel := context.WithCancel(context.Background())

	registerExecution(execID, cancel)

	go func() {
		defer cleanExecution(execID)

		for {
			select {
			case <-ctx.Done():
				logInfo("循环执行已停止 [ExecID:%s]", execID)
				return
			default:
				executeCommand(ctx, execID)
				if delay > 0 {
					time.Sleep(time.Duration(delay) * time.Second)
				}
			}
		}
	}()

	sendResponse(w, CommandResult{
		ExecID:   execID,
		Status:   "STARTED",
		Command:  command,
		Message:  "循环执行，每次间隔：" + strconv.Itoa(delay) + "秒",
		ExecTime: time.Now().Format(timeFormat),
	}, http.StatusOK)
}

func handleMultiple(w http.ResponseWriter, r *http.Request) {
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	delay, _ := strconv.Atoi(r.URL.Query().Get("delay"))
	execID := generateID()
	ctx, cancel := context.WithCancel(context.Background())

	registerExecution(execID, cancel)

	go func() {
		defer cleanExecution(execID)

		for i := 0; i < max(count, 1); i++ {
			select {
			case <-ctx.Done():
				logInfo("多次执行已停止 [ExecID:%s]", execID)
				return
			default:
				executeCommand(ctx, execID)
				if delay > 0 && i < count-1 {
					time.Sleep(time.Duration(delay) * time.Second)
				}
			}
		}
	}()

	sendResponse(w, CommandResult{
		ExecID:   execID,
		Status:   "STARTED",
		Command:  command,
		Message:  fmt.Sprintf("多次执行，执行次数：%d，每次间隔：%d秒", count, delay),
		ExecTime: time.Now().Format(timeFormat),
	}, http.StatusOK)
}

func handleStop(w http.ResponseWriter, r *http.Request) {
	execID := r.URL.Query().Get("exec_id")
	if execID == "" {
		sendError(w, "缺少exec_id参数", http.StatusBadRequest)
		return
	}

	execLock.Lock()
	defer execLock.Unlock()

	if exec, exists := executions[execID]; exists {
		exec.Cancel()
		exec.Stopped = true
		delete(executions, execID)
		logInfo("已停止执行 [ExecID:%s]", execID)
		sendResponse(w, CommandResult{ExecID: execID, Status: "STOPPED"}, http.StatusOK)
	} else {
		sendError(w, "无效的exec_id", http.StatusNotFound)
	}
}

func handleSingle(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	execID := generateID()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerExecution(execID, cancel)
	executeCommand(ctx, execID)
	cleanExecution(execID)
	duration := time.Since(startTime).Seconds()

	sendResponse(w, CommandResult{
		ExecID:     execID,
		Status:     "SUCCESS",
		Command:    command,
		Message:    "单次执行成功",
		ExecTime:   startTime.Format(timeFormat),
		ExecSecond: duration,
	}, http.StatusOK)
}

func executeCommand(ctx context.Context, execID string) {
	startTime := time.Now()
	var cmd *exec.Cmd

	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}

	output, err := cmd.CombinedOutput()
	duration := time.Since(startTime).Seconds()

	result := CommandResult{
		ExecID:     execID,
		Status:     "SUCCESS",
		Command:    command,
		ExecTime:   startTime.Format(timeFormat),
		ExecSecond: duration,
		Output:     string(output),
	}

	if err != nil {
		result.Status = "FAILED"
	}

	logJSON(result)
}

// 辅助函数
func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer "+token {
			logWarn("认证失败，收到请求头：%s", authHeader)
			sendError(w, "未授权", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func sendResponse(w http.ResponseWriter, data interface{}, code int) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(code)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(data); err != nil {
		logError("响应编码失败: %v", err)
	}
}

func sendError(w http.ResponseWriter, msg string, code int) {
	sendResponse(w, map[string]string{"error": msg}, code)
}

func registerExecution(id string, cancel context.CancelFunc) {
	execLock.Lock()
	defer execLock.Unlock()
	executions[id] = &Execution{ID: id, Cancel: cancel}
}

func cleanExecution(id string) {
	execLock.Lock()
	defer execLock.Unlock()
	delete(executions, id)
}

func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func getEndpoint() string {
	if endpoint != "" {
		return endpoint
	}
	return generateID()
}

func setupLogger() {
	time.Local = time.FixedZone("CST", 8*3600)
}

func logJSON(data interface{}) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err == nil {
		logInfo(strings.TrimSpace(buf.String()))
	}
}

// 日志相关函数
func logInfo(format string, v ...interface{}) {
	logMessage("INFO", format, v...)
}

func logWarn(format string, v ...interface{}) {
	logMessage("WARN", format, v...)
}

func logError(format string, v ...interface{}) {
	logMessage("ERROR", format, v...)
}

func logMessage(level, format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	_, file, line, _ := runtime.Caller(2)
	fmt.Printf("[%s][%s][PID:%d][%s:%d] %s\n",
		level,
		time.Now().Format(timeFormat),
		os.Getpid(),
		filepath.Base(file),
		line,
		msg)
}

func printHelp() {
	fmt.Printf(`
远程命令执行服务 v%s

使用方法:
  remotec -p 端口号 -c 命令 [选项]

参数说明:
  -p          string    监听的端口号 (必填)
  -c          string    要执行的系统命令 (必填)
  --token     string    认证token格式: Bearer <token>
  --endpoint  string    自定义端点路径
  -h, --help            显示帮助信息

请求参数:
  action      string    执行动作（loop, multiple, stop）
  delay       int       循环执行间隔 (秒）
  count       int       多次执行次数
  exec_id     string    执行ID（执行程序后获得）

示例:
  程序启动：remotec -p 8080 -c "ping 127.0.0.1" --token secret
  单次执行：curl 'http://localhost:8080/path'
  循环执行：curl 'http://localhost:8080/path?action=loop&delay=5'
  多次执行：curl 'http://localhost:8080/path?action=multiple&count=3'

`, appVersion)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
