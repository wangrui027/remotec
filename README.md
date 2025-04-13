# 远程命令执行服务

本程序实现远程 `http` 调用执行本地命令，支持单次执行、循环执行、多次执行，支持端点路径自动生成，支持定义 `token` 请求头

具体用法如下：

```bash
[root@aliyun-centos7 ~]# remotec

远程命令执行服务 v1.4.0

使用方法：
  remotec -p 端口号 -c 命令 [选项]

参数说明：
  -p          string    监听的端口号 (必填)
  -c          string    要执行的系统命令 (必填)
  --token     string    认证token格式: Bearer <token>
  --endpoint  string    自定义端点路径
  -h, --help            显示帮助信息

请求参数：
  action      string    执行动作（loop, multiple, stop）
  delay       int       循环执行间隔 (秒）
  count       int       多次执行次数
  exec_id     string    执行ID（执行程序后获得）

示例：
  程序启动：remotec -p 8080 -c "ping 127.0.0.1 -c 4" --token secret
  单次执行：curl 'http://localhost:8080/path'
  循环执行：curl 'http://localhost:8080/path?action=loop&delay=5'
  多次执行：curl 'http://localhost:8080/path?action=multiple&count=3'
  携带token：curl -H 'token: your_token' 'http://localhost:8080/path'

说明：
  1、单次执行和多次执行的结果会立即返回；
  2、循环执行时程序http响应会立即返回，执行结果通过日志输出；
  3、多次执行返回的output仅包含最后一次执行的结果。
```

