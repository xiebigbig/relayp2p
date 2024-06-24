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
	"os/signal"
	"time"
	"io"
	"sync"
	"strings"
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
	flag.PrintDefaults()
}

func init() {
	flag.Usage = usage
	
	flag.StringVar(&flagSpaces, "s", "default", "client: enabled addr spaces, 'default', 'all', 'public', or 'none' (force relay) ")
	flag.BoolVar(&flagWait, "w", false, "client: wait up to 5s for all p2p conns, for debugging")
	flag.BoolVar(&flagVerbose, "v", false, "print verbose logs")
	
	flag.StringVar(&remoteAddr, "r", "192.167.1.6:3306,192.167.1.6:8485,:5678", "remote addrs")
	flag.StringVar(&localAddr, "l", ":5002,:5003,:5004", "local addrs")
	
	flag.StringVar(&token, "token", "123456", "123456")
	flag.StringVar(&model, "m", "serve", "dial、d or accept、a or serve")
	flag.StringVar(&relayAddr, "rdv", "http://192.167.1.124:8686", "relayAddr")
	flag.StringVar(&flagLAddr, "addr", ":8686", "server: listening addr")
}
/*
    B:accept A:dial 
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
	    //remoteAddr
	    remoteAddrs := strings.Split(remoteAddr, ",")  
	    var wg sync.WaitGroup
	    wg.Add(len(remoteAddrs))
    	for i, addr := range remoteAddrs { 
    	    go func() {
    	        defer wg.Done()
        		err = clientCmd(client, addr, localAddr, fmt.Sprintf("%s:%d",token,i) ,rdv.DIAL)
            	if err != nil {
            		slog.Error("an error occurred", "err", err)
            	}
	    	}()
    	}  
    	wg.Wait()
	case "a", "accept":
	    isServer = true
	    //remoteAddr
	    localAddrs := strings.Split(localAddr, ",")  
	    var wg sync.WaitGroup
	    wg.Add(len(localAddrs))
    	for i, addr := range localAddrs {
    	    go func() {
    	        defer wg.Done()
        		err = clientCmd(client, remoteAddr, addr, fmt.Sprintf("%s:%d",token,i) ,rdv.ACCEPT)
            	if err != nil {
            		slog.Error("an error occurred", "err", err)
            	}
    	    }()
    	}
    	wg.Wait()
	default:
		usage()
		os.Exit(2)
	}
}

func clientCmd(client *rdv.Client, remoteAddr, localAddr, token, method string) error {
	for {
	    tStart := time.Now()
    	conn, _, err := client.Do(context.Background(), method, relayAddr, token, nil)
    	if err != nil {
    	    fmt.Printf("Error accepting connection: %v\n", err)  
    		time.Sleep(3 * time.Second)
			continue
    	}
    	obs := cmp.Or(conn.ObservedAddr, &netip.AddrPort{})
    	space := rdv.AddrSpaceFrom(obs.Addr())
    	if space != rdv.SpacePublic4 {
    		slog.Warn("client: expected observed to be public ipv4 (check server config)", "addr", conn.ObservedAddr)
    	}
    	var tConnected = time.Now()
    	slog.Info("client: peer connected", "is_relay", conn.IsRelay, "addr", conn.RemoteAddr(), "dur", tConnected.Sub(tStart))
    	
    	smuxConfig := smux.DefaultConfig()
    	smuxConfig.MaxReceiveBuffer = 4194304
    	smuxConfig.KeepAliveInterval = time.Duration(pingInterval) * time.Second
    	smuxConfig.KeepAliveTimeout = time.Duration(pingInterval) * time.Second * 3

    	// stream multiplex
    	var smuxSession *smux.Session
    	if isServer {
    	    // 一个连接通道，分多个连接对接 本地 Accept
    		smuxSession, err = smux.Server(conn, smuxConfig)
        	
            // 全局读取来自nat源的包
        	listenTcpAddr, err := net.ResolveTCPAddr("tcp4", localAddr)
        	checkError(err)
        	listener, err := net.ListenTCP("tcp4", listenTcpAddr)
        	checkError(err)
        	log.Println("listening on:", listener.Addr())
        	for{
        		p1, err := listener.AcceptTCP()
        		if err != nil {
        			log.Fatalln(err)
        			checkError(err)
        		}
        		go handleLocalTcp(smuxSession, p1, !flagVerbose)
        	}
    	} else {
    	    log.Println("TargetTcp on:", remoteAddr)
    	    // 一个连接通道，分多个连接对接 本地 dial
    		smuxSession, err = smux.Client(conn, smuxConfig)
    	    handleTargetTcp(remoteAddr, smuxSession, !flagVerbose)
    	}
	}
	return nil
}

//handleTargetTcp
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

//handleLocalTcp
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

//checkError
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

//handler
type handler struct{}

//Serve
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


