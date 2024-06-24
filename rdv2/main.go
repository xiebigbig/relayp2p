package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
// 	"sync"
	"os/signal"
	"time"
	"io"
	"github.com/xtaci/smux"
	"github.com/betamos/rdv"
)

var (
	flagVerbose bool
	flagWait    bool
	flagSpaces  string
	flagLAddr   string
	remoteAddr   string
	localAddr   string
	token   string
	model   string
	relayAddr string
	isServer = false
	pingInterval = 3
)

func usage() {
	fmt.Fprintf(flag.CommandLine.Output(), "usage:\n\trdv [ flags ] serve\n\trdv [ flags ] <dial|accept> ADDR TOKEN:\n\n")
	flag.PrintDefaults()
}

func init() {
	flag.Usage = usage
	flag.StringVar(&flagLAddr, "addr", ":8686", "server: listening addr")
	flag.StringVar(&flagSpaces, "s", "default", "client: enabled addr spaces, 'default', 'all', 'public', or 'none' (force relay) ")
	flag.BoolVar(&flagWait, "w", false, "client: wait up to 5s for all p2p conns, for debugging")
	flag.BoolVar(&flagVerbose, "v", false, "print verbose logs")
	flag.StringVar(&remoteAddr, "r", "192.167.1.6:3306", "client: remote addr")
	flag.StringVar(&localAddr, "l", "0.0.0.0:5002", "client: local addr")
	flag.StringVar(&token, "token", "123456", "123456")
	flag.StringVar(&model, "m", "serve", "dial accept serve")
	flag.StringVar(&relayAddr, "rdv", "192.167.1.124:8686", "relayAddr")
}

/*
    A :dial   A:accept
    B :dial   B:accept
    
    A:accept -> B :dial
    B:accept -> A :dial
*/

//函数入口
func main() {
	var err error
	flag.Parse()
	if flagVerbose {
		log.SetFlags(log.Lmicroseconds)
		slog.SetLogLoggerLevel(slog.LevelDebug)
	} else {
		log.SetFlags(log.Ltime)
	}
	client := &rdv.Client{Logger: slog.Default()}
	if flagWait {
		client.Picker = rdv.WaitConstant(5 * time.Second)
	}
	// 'all', 'public', or 'none'
	switch flagSpaces {
	case "default":
	case "all":
		client.AddrSpaces = rdv.AllSpaces
	case "public":
		client.AddrSpaces = rdv.PublicSpaces
	case "none":
		client.AddrSpaces = rdv.NoSpaces
	default:
		usage()
		os.Exit(2)
	}
	
	//serve dial  accept
	switch model {
	case "s", "serve":
		err = serverCmd(flagLAddr)
	case "d", "dial":
	    isServer = false
		err = clientCmd(client, rdv.DIAL)
	case "a", "accept":
	    isServer = true
		err = clientCmd(client, rdv.ACCEPT)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		slog.Error("an error occurred", "err", err)
	}
}

//客户端
func clientCmd(client *rdv.Client, method string) error {

	chTCPConn := make(chan *net.TCPConn, 16)
    //创建本地监听 服务 
	go tcpListener(chTCPConn)
	for{
	    //method dial
	    tStart := time.Now()
    	conn, _, err := client.Do(context.Background(), method, relayAddr, token, nil)
    	if err != nil {
    	    fmt.Printf("Error connection: %v\n", err)  
    		time.Sleep(2 * time.Second)
			continue
    	}
    	obs := cmp.Or(conn.ObservedAddr, &netip.AddrPort{})
    	space := rdv.AddrSpaceFrom(obs.Addr())
    	if space != rdv.SpacePublic4 {
    		slog.Warn("client: expected observed to be public ipv4 (check server config)", "addr", conn.ObservedAddr)
    	}
    	var tConnected = time.Now()
    	slog.Info("client: peer connected", "is_relay", conn.IsRelay, "addr", conn.RemoteAddr(), "dur", tConnected.Sub(tStart))
	    
		time.Sleep(1*time.Second)
		if err == nil{
		    // connection  listener.AcceptTCP()
			p2pHandle(conn, chTCPConn)
		}else{
			time.Sleep(time.Duration(pingInterval)*time.Second)
		}
	}
	
	return nil
}

//创建本地监听 tcp 1022 服务 
func tcpListener(chTCPConn chan *net.TCPConn){
	listenTcpAddr, err := net.ResolveTCPAddr("tcp4", localAddr)
	checkError(err)
	listener, err := net.ListenTCP("tcp4", listenTcpAddr)
	checkError(err)
	log.Println("listening on:", listener.Addr())
	log.Println("TargetTcp on:", remoteAddr)
	for{
		p1, err := listener.AcceptTCP()
		if err != nil {
			log.Fatalln(err)
			checkError(err)
		}
		chTCPConn <- p1
	}
}


//创建本地监听指定
func p2pHandle(kcpconn net.Conn, chTCPConn chan *net.TCPConn){
	smuxConfig := smux.DefaultConfig()
	smuxConfig.MaxReceiveBuffer = 4194304
	smuxConfig.KeepAliveInterval = time.Duration(pingInterval) * time.Second
	smuxConfig.KeepAliveTimeout = time.Duration(pingInterval) * time.Second * 3
	
	// stream multiplex
	var smuxSession *smux.Session
	var err any
	if isServer {
	    // 一个连接通道，分多个连接来单独对接 本地 Accept
		smuxSession, err = smux.Server(kcpconn, smuxConfig)
	} else {
	    // 一个连接通道，分多个连接来单独对接 本地 dial
		smuxSession, err = smux.Client(kcpconn, smuxConfig)
	}
	//打洞成功
	if err == nil {
		log.Println("NewP2pConn connection:", kcpconn.LocalAddr(), "-> ", kcpconn.RemoteAddr())
	}
	
    //目标服务80 转发到外网session
	go handleTargetTcp(remoteAddr, smuxSession, !flagVerbose)
	tickerCheck := time.NewTicker( time.Duration(pingInterval)*time.Second)
	defer tickerCheck.Stop()
	for {
		select {
		case p1 := <-chTCPConn:
		    // 本地服务1022 转发到外网session
			go handleLocalTcp(smuxSession, p1, !flagVerbose)
		case <-tickerCheck.C:
			if smuxSession.IsClosed(){
				log.Println("p2p session closed")
				return
			}
		}
	}
}

func handleTargetTcp(addr string, session *smux.Session, quiet bool) {
	for {
		p1, err := session.AcceptStream()
		if err != nil {
			log.Println(err)
			return
		}
		//打洞成功之后，使用 tcp 通信，， 做一个标识，网卡ip，key 指定转发，
		p2, err := net.DialTimeout("tcp", addr, 5*time.Second)
		
		if err != nil {
			p1.Close()
			log.Println(err)
			continue
		}
		go func() {
			if !quiet {
				log.Println("tcp client opened")
				log.Println("TargetTcp " , addr)
				defer log.Println("tcp client closed")
			}
			defer p1.Close()
			defer p2.Close()

			streamCopy := func(dst io.Writer, src io.ReadCloser) chan struct{} {
            	//输出命令行
				die := make(chan struct{})
				go func() {
				    io.Copy(dst, src)
					close(die)
				}()
				return die
			}

			select {
			case <-streamCopy(p1, p2):
			case <-streamCopy(p2, p1):
			}
			
		}()
		
	}
}

func handleLocalTcp(sess *smux.Session, p1 io.ReadWriteCloser, quiet bool) {
	if !quiet {
		log.Println("stream opened")
		defer log.Println("stream closed")
	}
	defer p1.Close()
	p2, err := sess.OpenStream()
	if err != nil {
		return
	}
	defer p2.Close()

	streamCopy := func(dst io.Writer, src io.ReadCloser) chan struct{} {
    	//输出命令行
		die := make(chan struct{})
		go func() {
		    io.Copy(dst, src)
			close(die)
		}()
		return die
	}

	select {
	case <-streamCopy(p1, p2):
	case <-streamCopy(p2, p1):
	}
}

func checkError(err error) {
	if err != nil {
		log.Printf("%+v\n", err)
		os.Exit(-1)
	}
}

//服务中继
func serverCmd(laddr string) error {
	server := &rdv.Server{
		Handler: handler{},
		Logger:  slog.Default(),
	}
	server.Start()
	defer server.Close()
	ln, err := net.Listen("tcp", laddr)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{Handler: server}
	httpSrv.RegisterOnShutdown(server.Shutdown)

	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)
	context.AfterFunc(ctx, func() {
		httpSrv.Close()
	})
	slog.Info("listening", "addr", laddr)
	return httpSrv.Serve(ln)
}

type handler struct{}

func (h handler) Serve(ctx context.Context, dc, ac *rdv.Conn) {
	r := new(rdv.Relayer)
	t0 := time.Now()
	err := r.Continue(ctx, dc, ac)
	dur := time.Since(t0).Round(time.Millisecond) // reduce noise with ms
	slog.Info("continue", "token", dc.Token, "dur", dur, "err", err)
	if err != nil {
		return
	}
	dn, an, err := r.Relay(ctx, ac, dc, dc, ac)
	slog.Info("relay", "token", dc.Token, "dial_bytes", dn, "accept_bytes", an, "err", err)
}


