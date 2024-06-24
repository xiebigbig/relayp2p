### Introduction

假设A开始给B的公网地址发送UDP数据的同时，给服务器S发送一个中继请求，要求B开始给A的公网地址发送UDP信息。A往B的输出信息会导致NAT A打开 一个A的内网地址与与B的外网地址之间的新通讯会话，B往A亦然。一旦新的UDP会话在两个方向都打开之后，客户端A和客户端B就能直接通讯， 而无须再通过引导服务器S了。


### QuickStart
```
./relayp2p
./relayp2p -m d  -r 192.167.1.6:3306
./relayp2p -m a  -l 0.0.0.0:5002

```


### Install from source

```
$ CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build
```

公网服务器实现打洞 p2p成功后设备A和设备B直接通信，根据token来匹配，访问设备A的5002端口就等价于访问设备B的3306端口，访问设备B的5002端口就等价于访问设备A的3306端口。

```
[root@VM-16-5-centos rdvp2p]# ./relayp2p -h
usage:
	rdv [ flags ] serve
	rdv [ flags ] <dial|accept> ADDR TOKEN:

  -addr string
    	server: listening addr (default ":8686")
  -l string
    	local addr (default ":5002")
  -m string
    	dial、d or accept、a or serve (default "serve")
  -r string
    	remote addr (default "192.167.1.6:3306")
  -rdv string
    	relayAddr (default "http://192.167.1.124:8686")
  -s string
    	client: enabled addr spaces, 'default', 'all', 'public', or 'none' (force relay)  (default "default")
  -token string
    	123456 (default "123456")
  -v	print verbose logs
  -w	client: wait up to 5s for all p2p conns, for debugging
You have mail in /var/spool/mail/root

```