### Introduction

假设A开始给B的公网地址发送数据的同时，给服务器S发送一个中继请求，要求B开始给A的公网地址发送信息。A往B的输出信息会导致NAT A打开 一个A的内网地址与与B的外网地址之间的新通讯会话，B往A亦然。一旦新的UDP会话在两个方向都打开之后，客户端A和客户端B就能直接通讯， 而无须再通过引导服务器S了。


### 多端口，穿透
```
# CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build

# ./relayp2p
# ./relayp2p -m d  -r "192.167.1.6:3306,:5678"
# ./relayp2p -m a  -l ":5002,:5003"
```


### help

```
[root@VM-16-5-centos p2p-demo]# ./relayp2p -h
  -addr string
    	server: listening addr (default ":8686")
  -l string
    	local addrs (default ":5002,:5003,:5004")
  -m string
    	dial、d or accept、a or serve (default "serve")
  -r string
    	remote addrs (default "192.167.1.6:3306,192.167.1.6:8485,:5678")
  -rdv string
    	relayAddr (default "http://192.167.1.124:8686")
  -s string
    	client: enabled addr spaces, 'default', 'all', 'public', or 'none' (force relay)  (default "default")
  -token string
    	123456 (default "123456")
  -v	print verbose logs
  -w	client: wait up to 5s for all p2p conns, for debugging

```