package smtp

import (
	"encoding/json"
	"fmt"
  log "github.com/Sirupsen/logrus"
	"os"
	"path"
	"strings"
)

type routes map[string]map[string][]string

type Settings struct {
	Bind      string
	Port      int
	MaxCli    int
	DebugMode bool
	Spool     string
	AuditLog  string
	OpenRelay []string
	Routing   routes
	Gateways  []string
	Retries   []int
	SendLock  int
	fileName  string
	expire    int
	r_int     routes //list members (allowed senders)
	r_ext     routes //recipients opened to outside 
	*log.Logger
}

func (s *Settings) SetVerbose() {
  s.Level = log.DebugLevel
}

func (s Settings) Dump() string {
	return fmt.Sprintf("smtp@%s:%d, dbg=%v, cfg=%s", s.Bind, s.Port, s.DebugMode, s.fileName)
}

func (s *Settings) compileRoutes() {
	for domain, route := range s.Routing {
		s.r_int[domain] = make(map[string][]string)
		for alias, expn := range route {
			if alias == "@" {
				continue
			}
			for _, addr := range expn {
				if strings.Index(addr, "@") >= 0 {
					s.r_int[domain][addr] = nil
				}
			}
		}
		open_routes, ok := route["@"]
		if ok {
			s.r_ext[domain] = make(map[string][]string)
			for _, addr := range open_routes {
				s.r_ext[domain][addr] = nil
			}
		}
	}
}

func LoadSettings(filename string) (*Settings, error) {
	//ident := fmt.Sprintf("%s[%d]", path.Base(os.Args[0]), os.Getpid())
	logger := log.New()
	s := Settings{
		"127.0.0.1",       //Bind
		25,                //Port
		1,                 //MaxCli
		false,             //DebugMode
		"/var/spool/mail", //Spool
		"/var/log/mld",    //AuditLog
		[]string{},        //OpenRelay
		routes{},          //Routing
		[]string{},        //Gateways
		[]int{},           //Retries
		3600,              //SendLock
		filename,
		0,        //expire
		routes{}, //r_int
		routes{}, //r_ext
		logger,
	}
	var f *os.File
  f, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			f, err = os.Create(filename)
			if err == nil {
				defer f.Close()
				s.Routing = routes{
					"example.com": {
						"@":          {"postmaster"},
						"postmaster": {"admin@example.com"},
					},
				}
				s.Retries = []int{
					900, 1800, 3600, 7200, 14400, 28800, 57600,
				}
				js, err := json.MarshalIndent(s, "", "\t")
				if err == nil {
					f.Write(js)
				}
			}
		}
	} else {
		defer f.Close()
		dec := json.NewDecoder(f)
		err = dec.Decode(&s)
	}
	if err == nil {
		//s.Mode(s.DebugMode)
		s.Spool = path.Clean(s.Spool)
		err = os.MkdirAll(s.Spool+"/inbound", 0755)
		if err == nil {
			err = os.MkdirAll(s.Spool+"/outbound", 0755)
		}
		if err == nil && s.AuditLog != "" {
			s.AuditLog = path.Clean(s.AuditLog)
			err = os.MkdirAll(s.AuditLog, 0755)
		}
	}
	if err == nil {
		if s.MaxCli <= 0 {
			s.MaxCli = 1
		}
		if s.SendLock < 3600 {
			s.SendLock = 3600
		}
		s.expire = 0
		for _, d := range s.Retries {
			s.expire += d
		}
		s.expire *= 2
		if s.expire < 0 {
			s.Retries = []int{900, 1800, 3600, 7200, 14400, 28800, 57600}
			s.expire = 228600
		} else if s.expire < 3600 {
			s.expire = 3600
		}
		s.compileRoutes()
	}
	return &s, err
}
