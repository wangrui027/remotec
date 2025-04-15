package main

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"gopkg.in/yaml.v3"
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

const (
	timeFormat  = "2006-01-02 15:04:05"
	contentType = "application/json; charset=utf-8"
)

type AppConfig struct {
	Version string `yaml:"version"`
}

//go:embed config.yml
var embeddedConfig []byte

var (
	appConfig   AppConfig
	port        string
	command     string
	token       string
	endpoint    string
	showHelp    bool
	showVersion bool
)

type Execution struct {
	ID      string
	Cancel  context.CancelFunc
	Stopped bool
}

var (
	execLock   sync.Mutex
	executions = make(map[string]*Execution)
)

type CommandResult struct {
	ExecID     string  `json:"exec_id"`
	Status     string  `json:"status"`
	Command    string  `json:"command"`
	Message    string  `json:"message"`
	ExecTime   string  `json:"exec_time"`
	ExecSecond float64 `json:"exec_second"`
	Output     string  `json:"output"`
}

// POST请求参数结构体
type RequestParams struct {
	Action string `json:"action"`
	Delay  int    `json:"delay"`
	Count  int    `json:"count"`
	ExecID string `json:"exec_id"`
}

func init() {
	flag.StringVar(&port, "p", "", "监听的端口号")
	flag.StringVar(&command, "c", "", "要执行的命令")
	flag.StringVar(&token, "token", "", "认证token")
	flag.StringVar(&endpoint, "endpoint", "", "自定义端点路径")
	flag.BoolVar(&showVersion, "v", false, "显示版本号")
	flag.BoolVar(&showHelp, "help", false, "显示帮助信息")
}

func main() {
	initAppConfig()
	flag.Parse()
	setupLogger()

	if showVersion {
		fmt.Println(appConfig.Version)
		return
	}

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

func initAppConfig() {
	if len(embeddedConfig) == 0 {
		appConfig.Version = "unknown" // 默认版本号
		return
	}

	if err := yaml.Unmarshal(embeddedConfig, &appConfig); err != nil {
		logWarn("解析配置文件失败: %v", err)
		appConfig.Version = "unknown" // 解析失败时设置默认版本号
	}
}

func startServer() {
	endpointPath := getEndpoint()
	url := fmt.Sprintf("http://localhost:%s/%s", port, endpointPath)

	handler := http.HandlerFunc(requestHandler)
	if token != "" {
		handler = tokenAuthMiddleware(handler)
	}

	http.HandleFunc("/"+endpointPath, handler)
	logInfo("服务启动成功，监听地址：%s", url)
	if token != "" {
		logInfo("token已设置，接口调用时需传递请求头：'token: %s'", token)
	}

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		logError("服务器启动失败: %v", err)
		os.Exit(1)
	}
}

func tokenAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqToken := r.Header.Get("token")
		if token != "" && reqToken != token {
			logWarn("认证失败，未收到正确的token")
			sendError(w, "未授权", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func requestHandler(w http.ResponseWriter, r *http.Request) {
	// 支持GET和POST方法
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		sendError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	var params RequestParams
	var err error

	// 解析请求参数
	if r.Method == http.MethodGet {
		// 从查询参数解析
		params.Action = r.URL.Query().Get("action")
		params.Delay, _ = strconv.Atoi(r.URL.Query().Get("delay"))
		params.Count, _ = strconv.Atoi(r.URL.Query().Get("count"))
		params.ExecID = r.URL.Query().Get("exec_id")
	} else {
		// 从JSON body解析
		defer r.Body.Close()
		if err = json.NewDecoder(r.Body).Decode(&params); err != nil {
			sendError(w, "无效的JSON格式", http.StatusBadRequest)
			return
		}
		// 处理可能缺失的字段（默认值处理）
		if params.Action == "" {
			params.Action = "single" // 默认行为，类似原逻辑
		}
	}

	switch params.Action {
	case "multiple":
		handleMultiple(w, r, params)
	case "loop":
		handleLoop(w, r, params)
	case "stop":
		handleStop(w, r, params)
	case "stopAll":
		handleStopAll(w, r)
	default:
		handleSingle(w, r, params)
	}
}

func handleStopAll(w http.ResponseWriter, r *http.Request) {
	execLock.Lock()
	defer execLock.Unlock()

	stoppedCount := 0
	for id, execution := range executions {
		execution.Cancel()
		execution.Stopped = true
		stoppedCount++
		logInfo("已停止执行 [ExecID:%s]", id)
	}

	executions = make(map[string]*Execution)

	sendResponse(w, CommandResult{
		Status:  "STOPPED_ALL",
		Message: fmt.Sprintf("已停止%d个正在执行的任务", stoppedCount),
	}, http.StatusOK)
}

func handleLoop(w http.ResponseWriter, r *http.Request, params RequestParams) {
	delay := params.Delay
	execID := generateID()
	ctx, cancel := context.WithCancel(context.Background())

	registerExecution(execID, cancel)

	go func() {
		defer cleanExecution(execID)

		for {
			select {
			case <-ctx.Done():
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
		Message:  fmt.Sprintf("循环执行，间隔：%d秒", delay),
		ExecTime: time.Now().Format(timeFormat),
	}, http.StatusOK)
}

func handleMultiple(w http.ResponseWriter, r *http.Request, params RequestParams) {
	count := max(params.Count, 1)
	delay := params.Delay
	execID := generateID()
	ctx, cancel := context.WithCancel(context.Background())

	registerExecution(execID, cancel)
	defer cleanExecution(execID)

	startTime := time.Now()
	var result CommandResult

	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			logInfo("多次执行已停止 [ExecID:%s]", execID)
			return
		default:
			result = executeCommand(ctx, execID)
			if delay > 0 && i < count-1 {
				time.Sleep(time.Duration(delay) * time.Second)
			}
		}
	}

	sendResponse(w, CommandResult{
		ExecID:     execID,
		Status:     "COMPLETED",
		Command:    command,
		Message:    fmt.Sprintf("多次执行，次数：%d，间隔：%d秒", count, delay),
		ExecTime:   time.Now().Format(timeFormat),
		ExecSecond: time.Since(startTime).Seconds(),
		Output:     result.Output,
	}, http.StatusOK)
}

func handleStop(w http.ResponseWriter, r *http.Request, params RequestParams) {
	execID := params.ExecID
	if execID == "" {
		sendError(w, "缺少exec_id参数", http.StatusBadRequest)
		return
	}

	execLock.Lock()
	defer execLock.Unlock()

	if execution, exists := executions[execID]; exists {
		execution.Cancel()
		execution.Stopped = true
		delete(executions, execID)
		logInfo("已停止执行 [ExecID:%s]", execID)
		sendResponse(w, CommandResult{ExecID: execID, Status: "STOPPED"}, http.StatusOK)
	} else {
		sendError(w, "无效的exec_id", http.StatusNotFound)
	}
}

func handleSingle(w http.ResponseWriter, r *http.Request, params RequestParams) {
	startTime := time.Now()
	execID := generateID()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerExecution(execID, cancel)
	result := executeCommand(ctx, execID)
	cleanExecution(execID)
	duration := time.Since(startTime).Seconds()

	sendResponse(w, CommandResult{
		ExecID:     execID,
		Status:     "COMPLETED",
		Command:    command,
		Message:    "单次执行",
		ExecTime:   startTime.Format(timeFormat),
		ExecSecond: duration,
		Output:     result.Output,
	}, http.StatusOK)
}

func executeCommand(ctx context.Context, execID string) CommandResult {
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
		Status:     "COMPLETED",
		Command:    command,
		ExecTime:   startTime.Format(timeFormat),
		ExecSecond: duration,
		Output:     string(output),
	}

	if err != nil {
		result.Status = "FAILED"
	}

	logJSON(result)

	return result
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
		time.Now().Format(timeFormat),
		level,
		os.Getpid(),
		filepath.Base(file),
		line,
		msg)
}

func printHelp() {
	fmt.Printf(`
远程命令执行服务 %s

程序启动：
  remotec -p 端口号 -c 命令 [选项]

选项列表：
  -p            string    监听的端口号 (必填)
  -c            string    要执行的系统命令 (必填)
  --token       string    认证token (选填)
  --endpoint    string    自定义端点路径 (选填)
  -v                      显示版本号
  --help                  显示帮助信息

程序启动示例：
  remotec -p 8080 -c "ping 127.0.0.1 -c 2" --token your_token

接口请求参数：
  action      string    执行动作（multiple、loop、stop、stopAll）
  delay       int       循环执行间隔（秒）
  count       int       多次执行次数
  exec_id     string    执行ID（请求返回中获得）

GET请求示例：
  单次执行：curl 'http://localhost:8080/path'
  多次执行：curl 'http://localhost:8080/path?action=multiple&count=3'
  循环执行：curl 'http://localhost:8080/path?action=loop&delay=5'
  停止执行：curl 'http://localhost:8080/path?action=stop&exec_id=xxx'
  停止所有：curl 'http://localhost:8080/path?action=stopAll'
  携带token：curl -H 'token: your_token' 'http://localhost:8080/path'

POST请求示例：
  curl -X POST -H "Content-Type: application/json" -H "token: your_token" \
  -d '{"action":"loop","delay":5}' http://localhost:8080/path

其他请求示例与GET方式类似，只需将参数放入JSON body即可。

使用说明：
  1、单次执行和多次执行的结果随Response返回；
  2、多次执行返回的output为最后一次执行的结果；
  3、循环执行时Response会立即返回，执行结果通过日志输出；
`, appConfig.Version)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
