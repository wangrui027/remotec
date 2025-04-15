# 远程命令执行服务

本程序实现远程 `http` 调用执行服务端命令，支持单次执行、多次执行，循环执行，支持端点路径自动生成，支持定义 `token` 请求头，可指定 `exec_id` 停止正在执行的任务，可一次性停止所有正在执行的任务。

具体用法如下：

```bash
[root@aliyun-centos7 ~]# remotec

远程命令执行服务 v1.11.0

使用方法：
  remotec -p 端口号 -c 命令 [选项]

程序启动参数：
  -p          string    监听的端口号 (必填)
  -c          string    要执行的系统命令 (必填)
  --token     string    认证token (选填)
  --endpoint  string    自定义端点路径 (选填)
  -h, --help            显示帮助信息

接口请求参数：
  action      string    执行动作（multiple、loop、stop、stopAll）
  delay       int       循环执行间隔（秒）
  count       int       多次执行次数
  exec_id     string    执行ID（请求返回中获得）

接口请求示例：
  程序启动：remotec -p 8080 -c "ping 127.0.0.1 -c 2" --token secret
  单次执行：curl 'http://localhost:8080/path'
  多次执行：curl 'http://localhost:8080/path?action=multiple&count=3'
  循环执行：curl 'http://localhost:8080/path?action=loop&delay=5'
  停止执行：curl 'http://localhost:8080/path?action=stop&exec_id=xxx'
  停止所有：curl 'http://localhost:8080/path?action=stopAll'
  携带token：curl -H 'token: your_token' 'http://localhost:8080/path'

程序说明：
  1、单次执行和多次执行的结果随Response返回；
  2、多次执行返回的output为最后一次执行的结果；
  3、循环执行时Response会立即返回，执行结果通过日志输出；
```

