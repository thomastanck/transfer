package main

import (
	"fmt"
	"github.com/julienschmidt/httprouter"
	"github.com/thomastanck/transfer/util"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// Types and shit

type transferStatus int

const (
	statusWaiting transferStatus = iota
	statusReady
	statusTransferring
	statusCompleted
	statusError
)

type httpConnection struct {
	w http.ResponseWriter
	r *http.Request
}

type session struct {
	token  string
	status transferStatus

	upstreamCloser   chan bool
	downstreamCloser chan bool

	timeoutTimer *time.Timer

	upstream   *httpConnection
	downstream *httpConnection
}

// Make sure you lock sessionsMut before calling this
func (s *session) setUpstream(w http.ResponseWriter, r *http.Request) bool {
	if s.upstream == nil {
		s.upstream = &httpConnection{w, r}
		log.Printf("\t%s\tUpstream connected\n", s.token)

		if s.downstream != nil {
			s.status = statusReady
			util.StopAndConsumeTimer(s.timeoutTimer)
			go s.startTransfer()
		}
		return true
	} else {
		return false
	}
}

// Make sure you lock sessionsMut before calling this
func (s *session) setDownstream(w http.ResponseWriter, r *http.Request) bool {
	if s.downstream == nil {
		s.downstream = &httpConnection{w, r}
		log.Printf("\t%s\tDownstream connected\n", s.token)

		if s.upstream != nil {
			s.status = statusReady
			util.StopAndConsumeTimer(s.timeoutTimer)
			go s.startTransfer()
		}
		return true
	} else {
		return false
	}
}

func (s *session) startTransfer() {
	if s.status != statusReady {
		panic("session status not ready")
	}

	log.Printf("\t%s\tStarting transfer\n", s.token)

	// Set headers
	contentlength := s.upstream.r.Header.Get("Content-Length")
	s.downstream.w.Header().Set("Content-Length", contentlength)
	s.downstream.w.Header().Set("Content-Disposition", "attachment")

	s.status = statusTransferring

	// Copy from upstream body to downsteam response. Easy!
	_, err := io.Copy(s.downstream.w, s.upstream.r.Body)

	if err == nil {
		s.status = statusCompleted
	} else {
		s.status = statusError
	}

	log.Printf("\t%s\tTransfer completed\n", s.token)

	// Makes it inaccessible, should mean that it gets garbage collected.
	sessionsMut.Lock()
	s.upstreamCloser <- err != nil
	s.downstreamCloser <- err != nil
	delete(sessions, s.token)
	sessionsMut.Unlock()
}

// Globals

var sessionsMut sync.Mutex
var sessions map[string]*session

// Handlers

func index(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	http.ServeFile(w, r, "index.html")
}

func sessionTimeoutHandler(s session) {
	for {
		sessionsMut.Lock()
		select {
		case <-s.timeoutTimer.C:
			s.upstreamCloser <- true
			s.downstreamCloser <- true
			delete(sessions, s.token)
			log.Printf("\t%s\tTimeout. Num sessions: %d\n", s.token, len(sessions))
		default:
		}
		sessionsMut.Unlock()
	}
}

func newsession(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	token, err := util.GenerateRandomString(48)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "Error when generating random token")
		return
	}
	session := session{
		token:            token,
		upstreamCloser:   make(chan bool, 1),
		downstreamCloser: make(chan bool, 1),
		timeoutTimer:     time.NewTimer(time.Second * 60),
	}
	sessionsMut.Lock()
	sessions[token] = &session
	log.Printf("\t%s\tToken created. Num sessions: %d\n", token, len(sessions))
	sessionsMut.Unlock()

	// Timeout handler
	go sessionTimeoutHandler(session)

	io.WriteString(w, token)
}

func dropConnection(w http.ResponseWriter) {
	if wr, ok := w.(http.Hijacker); ok {
		conn, _, err := wr.Hijack()
		if err != nil {
			fmt.Fprint(w, err)
		}
		conn.Close()
	}
}

func up(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	token := ps[0].Value
	defer r.Body.Close()
	defer log.Printf("\t%s\tClosing upstream connection", token)
	sessionsMut.Lock()
	session := sessions[token]
	if session == nil {
		sessionsMut.Unlock()
		log.Printf("\t%s\tSession does not exist", token)
		dropConnection(w)
		return
	}
	if !session.setUpstream(w, r) {
		log.Printf("\t%s\tCould not connect to upstream channel", token)
		dropConnection(w)
		return
	}
	util.RefreshTimer(session.timeoutTimer, time.Second * 300)
	sessionsMut.Unlock()
	err := <-session.upstreamCloser
	if err {
		dropConnection(w)
		return
	}
}

func down(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	token := ps[0].Value
	r.Body.Close()
	defer log.Printf("\t%s\tClosing downstream connection", token)
	sessionsMut.Lock()
	session := sessions[token]
	if session == nil {
		sessionsMut.Unlock()
		log.Printf("\t%s\tSession does not exist", token)
		dropConnection(w)
		return
	}
	if !session.setDownstream(w, r) {
		log.Printf("\t%s\tCould not connect to downstream channel", token)
		dropConnection(w)
		return
	}
	util.RefreshTimer(session.timeoutTimer, time.Second * 300)
	sessionsMut.Unlock()
	err := <-session.downstreamCloser
	if err {
		dropConnection(w)
		return
	}
}

// main

func main() {
	sessionsMut.Lock()
	sessions = make(map[string]*session)
	sessionsMut.Unlock()

	router := httprouter.New()
	router.GET("/", index)
	router.GET("/newsession", newsession)
	router.PUT("/up/:token", up)
	router.PUT("/up/:token/*filename", up)
	router.GET("/down/:token", down)
	router.GET("/down/:token/*filename", down)

	log.Fatal(http.ListenAndServe(":8085", router))
}
