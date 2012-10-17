package smtp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log4g"
	"math/rand"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	PROC_QUEUED = iota //add mail to queue
	PROC_SUBMIT        //confirm relay of queue (move inbound to outbound)
	PROC_FLUSH         //discard queued mail for this session
)

func normalize(addr string) (string, string) {
	cmd := ""
	parts := strings.Split(addr, ":")
	if len(parts) >= 2 {
		cmd = strings.ToUpper(strings.TrimSpace(parts[0]))
		parts = strings.SplitN(parts[len(parts)-1], "<", 2)
		addr = strings.TrimSpace(strings.SplitN(parts[len(parts)-1], ">", 2)[0])
		parts = strings.SplitN(addr, "@", 2)
		if len(parts) > 1 && len(parts[1]) > 0 {
			addr = parts[0] + "@" + strings.ToLower(parts[1])
		} else {
			addr = parts[0]
		}
	}
	return cmd, addr
}

func testFile(fn string, flag int, perm os.FileMode) (res byte, err error) {
	f, err := os.OpenFile(fn, flag, perm)
	if err == nil {
		f.Close()
		res = 0
	} else {
		switch {
		case os.IsPermission(err):
		    res = 1
		case os.IsNotExist(err):
		    res = 2
		case os.IsExist(err):
		    res = 3
		default:
		    res = 4  
		}
	}
	return 
}

func sendMail(env string, logger log4g.Logger) {
	ef, err := os.OpenFile(env, os.O_RDWR, 0600)
	if err != nil {
		logger.Log("RUNERR: " + err.Error())
		return
	}
	defer ef.Close()
	/*
	msg := []byte(env)
	copy(msg[len(msg)-3:len(msg)], "msg")
	fmsg, err := os.OpenFile(string(msg), os.O_RDWR, 0600)
	if err != nil {
		logger.Log("RUNERR: " + err.Error())
		logger.Log("  Deleting empty/invalid envelope: " + env)
		err = os.Remove(env)
		if err != nil {
			logger.Log("  RUNERR: " + err.Error())
		}
		return
	}
	fmsg.Close()
	*/
	dec := json.NewDecoder(ef)
	route := make(map[string]map[string]int64)
	if err = dec.Decode(&route); err != nil {
		logger.Log("RUNERR: " + err.Error())
		return
	}
	now := time.Now().Unix()
	sched := route["STATUS"]["schedule"]
	logger.Debugf("%s: schedued=%d, now=%d", path.Base(env[:len(env)-4]), sched, now)
	if sched > now {
		return
	}
	route["STATUS"]["schedule"] = now + 3600 //by default only retry after 1 hour
    tmpfile := os.TempDir() + "/" + path.Base(env)
	tf, err := os.Create(tmpfile)
	if err != nil {
		logger.Log("RUNERR: " + err.Error())
		return
	}	
	defer tf.Close()
	enc := json.NewEncoder(tf)
	if err = enc.Encode(&route); err != nil {
		logger.Log("RUNERR: " + err.Error())
		return	
	}
	ef.Close()
	tf.Close()
    err = os.Rename(tmpfile, env)
    if err != nil {
		logger.Log("RUNERR: " + err.Error())
		return
	}
	logger.Log("TODO: send - " + env)
}

func SendMails(spool string, logger log4g.Logger) {
	envelopes, err := filepath.Glob(spool + "/*.env")
	if err == nil {
		logger.Debugf("SendMails: queued_messages=%v", len(envelopes))
		for _, e := range envelopes {
			go sendMail(e, logger)
		}
	} else {
		logger.Log("RUNERR: " + err.Error())
	}
}

type SmtpError string

func (e SmtpError) Error() string {
	return string(e)
}

type Session struct {
	conn       net.Conn
	path       string
	state      byte
	seq        int
	sender     string
	recipients map[string]byte
	file       *os.File
	p_errs     byte //protocol errors (e.g. syntex error, command out-of-order)
	r_errs     byte //relay errors
	*Settings
}

func (s Session) expects() (reply string) {
	reply = "503 Bad sequence of commands"
	cmds := ""
	switch s.state {
	case 1:
		cmds = "EHLO, HELO"
	case 2:
		cmds = "MAIL"
	default:
		if len(s.recipients) == 0 {
			cmds = "RCPT"
		}
	}
	if len(cmds) > 0 {
		reply += ", expecting: " + cmds
	}
	return
}

func (s Session) expnList(ctrl map[string][]string, list []string) {
	for _, r := range list {
		at := strings.Index(r, "@")
		if at > 0 && at < len(r)-1 {
			s.Debugf("%s>   =>%s", s.CliAddr(), r)
			s.recipients[r] = 1
		} else {
			expn, ok := ctrl[r]
			if ok {
				s.Debugf("%s>   =>[%s, %d addr(s)]", s.CliAddr(), r, len(expn))
				s.expnList(ctrl, expn)
			} else {
				s.Log("CFGERR: Unresolved recpient: " + r)
			}
		}
	}
}

func (s *Session) relay(addr string) string {
	parts := strings.SplitN(addr, "@", 2)
	if len(parts) < 2 {
		return "Relay denied for " + addr
	}
	ctrl, ok := s.RelayCtrl[parts[1]]
	if !ok {
		return "Relay denied for " + addr
	}
	expn, ok := ctrl[parts[0]]
	if !ok {
		return "Relay denied for " + addr
	}
	rcpts, ok := ctrl[s.sender]
	if !ok {
		return "Relay denied for " + s.sender
	}
	if len(rcpts) > 0 {
		noMatch := true
		for _, u := range rcpts {
			if u == parts[0] {
				noMatch = false
				break
			}
		}
		if noMatch {
			return "Relay denied for " + s.sender
		}
	}
	s.expnList(ctrl, expn)
	return ""
}

func (s Session) CliAddr() string {
	return s.conn.RemoteAddr().String()
}

func (s Session) svrAddr() string {
	return strings.Split(s.conn.LocalAddr().String(), ":")[0]
}

func (s *Session) Reset(reason byte) {
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
	s.state = 2
	s.sender = ""
	s.recipients = make(map[string]byte)
	idir := s.Spool + "/inbound/" + s.path + "/"
	odir := s.Spool + "/outbound/"
	switch reason {
	case PROC_SUBMIT:
		os.MkdirAll(odir, 0777)
		dir, err := os.Open(idir)
		if err == nil {
			msgs, err := dir.Readdirnames(0)
			if err != nil {
				s.Log("PROC_SUBMIT_READDIR: " + err.Error())
			}
			s.Debugf("Queueing %d file(s)...", len(msgs))
			envs := 0
			for _, fn := range msgs {
				if strings.HasSuffix(fn, ".env") {
					envs++
				}
				fi := idir + fn
				fo := odir + s.path + "." + fn
				s.Debugf("  %s => %s", fi, fo)
				err = os.Rename(fi, fo)
				if err != nil {
					s.Logf("PROC_SUBMIT_MOVEFILE(%s): %s", fi, err.Error())
				}
			}
			s.Debugf("Message(s) queued: %d", envs)
		} else if !os.IsNotExist(err) {
			s.Log("PROC_SUBMIT_OPENDIR: " + err.Error())
		}
	case PROC_FLUSH:
		os.RemoveAll(s.Spool + "/inbound/" + s.path)
	}
}

func (s *Session) prep() (err error) {
	err = os.MkdirAll(s.Spool+"/inbound/"+s.path, 0777)
	if err != nil {
		return
	}
	file, err := os.Create(fmt.Sprintf("%s/inbound/%s/%d.env", s.Spool, s.path, s.seq))
	if err == nil {
		defer file.Close()
		route := make(map[string]map[string]int64)
		route["STATUS"] = map[string]int64{
			"schedule": 0,
		}
		route["SENDER"] = map[string]int64{s.sender: 0}
		route["DLERRS"] = make(map[string]int64)
		for r, _ := range s.recipients {
			p := strings.SplitN(r, "@", 2)
			route["DLERRS"][p[1]] = 0
			_, ok := route[p[1]]
			if !ok {
				route[p[1]] = make(map[string]int64)
			}
			route[p[1]][p[0]] = 0
		}
		enc := json.NewEncoder(file)
		if err = enc.Encode(&route); err == nil {
			s.file, err = os.Create(fmt.Sprintf("%s/inbound/%s/%d.msg", s.Spool, s.path, s.seq))
			if err == nil {
				_, err = s.file.Write([]byte("Received: from " + strings.Split(s.CliAddr(), ":")[0] + " by " + s.svrAddr() + "; " + time.Now().String()))
			}
		}
	}
	return
}

func (s *Session) handle(cmdline []byte) string {
	cmdstr := string(cmdline)
	if s.state < 4 {
		chunks := strings.SplitN(cmdstr, " ", 2)
		cmd := strings.ToUpper(chunks[0])
		param := ""
		if len(chunks) > 1 {
			param = chunks[1]
		}
		s.Debug(s.CliAddr() + "> " + cmdstr)
		switch cmd {
		case "EHLO", "HELO":
			s.state = 2
			return "250 At your service"
		case "DATA":
			if s.state < 3 {
				s.p_errs++
				return s.expects()
			}
			err := s.prep()
			if err == nil {
				s.state = 4
				return "354 Go ahead"
			}
			s.Logf("%s: ERROR! %s", s.CliAddr(), err.Error())
			s.state = 0
			return "421 Service temporarily unavailable"
		case "MAIL":
			if s.state < 2 {
				s.p_errs++
				return s.expects()
			}
			cmd, addr := normalize(param)
			if cmd == "FROM" {
				s.Debugf("%s>   =[%s]", s.CliAddr(), addr)
				s.sender = addr
				s.state = 3
				return "250 OK"
			} else {
				s.p_errs++
				return "500 Syntax error"
			}
		case "NOOP":
			return "250 OK"
		case "RCPT":
			if s.state < 3 {
				s.p_errs++
				return s.expects()
			}
			cmd, addr := normalize(param)
			if cmd == "TO" {
				s.Debugf("%s>   =[%s]", s.CliAddr(), addr)
				if msg := s.relay(addr); len(msg) > 0 {
					s.r_errs++
					s.Reset(PROC_FLUSH)
					return "553 " + msg
				}
				return "250 OK"
			} else {
				s.p_errs++
				return "500 Syntax error"
			}
		case "QUIT":
			s.Reset(PROC_SUBMIT)
			s.state = 0
			return "220 closing connection"
		case "RSET":
			s.Reset(PROC_FLUSH)
			return "250 Flushed"
		default:
			s.p_errs++
			return "502 Command not implemented"
		}
	} else {
		s.Debug(s.CliAddr() + "> " + cmdstr)
		if cmdstr == "." {
			s.Reset(PROC_QUEUED)
			return "250 OK"
		} else {
			s.file.Write([]byte("\r\n" + cmdstr))
		}
	}
	return ""
}

func (s *Session) Serve() error {
	br := bufio.NewReader(s.conn)
	for {
		s.conn.SetDeadline(time.Now().Add(5 * time.Minute))
		cmd, xl, err := br.ReadLine()
		if err != nil {
			return err
		}
		if xl {
			return SmtpError("Line too long")
		}
		reply := s.handle(cmd)
		if len(reply) > 0 {
			s.Debug(s.CliAddr() + "< " + string(reply))
			s.conn.Write([]byte(reply + "\r\n"))
		}
		if s.state <= 0 || s.p_errs > 2 || s.r_errs > 2 {
			if s.p_errs > 0 || s.r_errs > 0 {
				s.Logf("%s: ERROR! P=%d, R=%d", s.CliAddr(), s.p_errs, s.r_errs)
			}
			break
		}
	}
	return nil
}

func NewSession(conn net.Conn, env *Settings) (*Session, error) {
	now := int64(time.Now().UnixNano() / 1000)
	rand.Seed(now)
	sec := now / 1000000
	mic := now % 1000000
	path := fmt.Sprintf("%x.%x%x", sec, mic, rand.Intn(256))
	err := os.MkdirAll(env.Spool+"/inbound/"+path, 0777)
	if err != nil {
		return nil, err
	}
	session := &Session{
		conn,
		path,
		1,  //state
		1,  //seq
		"", //sender
		make(map[string]byte), //recipients
		nil,                   //file
		0,                     //p_errs
		0,                     //r_errs
		env,                   //Settings
	}
	_, err = conn.Write([]byte("220 Service ready\r\n"))
	return session, err
}
