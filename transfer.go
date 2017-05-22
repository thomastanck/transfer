package main

import (
	"github.com/bouk/httprouter"
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

	upstreamCloser   chan struct{}
	downstreamCloser chan struct{}

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
			go s.startTransfer()
		} else {
			util.RefreshTimer(s.timeoutTimer, time.Second*300)
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
			go s.startTransfer()
		} else {
			util.RefreshTimer(s.timeoutTimer, time.Second*300)
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
		log.Printf("\t%s\tTransfer completed\n", s.token)
	} else {
		s.status = statusError
		log.Printf("\t%s\tTransfer ended with error: %s\n", s.token, err)
	}

	// Makes it inaccessible, should mean that it gets garbage collected.
	sessionsMut.Lock()
	close(s.upstreamCloser)
	close(s.downstreamCloser)
	delete(sessions, s.token)
	sessionsMut.Unlock()
}

// Globals

var sessionsMut sync.Mutex
var sessions map[string]*session

// Handlers

func index(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "index.html")
}

func sessionTimeoutHandler(s *session) {
	<-s.timeoutTimer.C
	sessionsMut.Lock()
	defer sessionsMut.Unlock()
	if s.status == statusWaiting {
		close(s.upstreamCloser)
		close(s.downstreamCloser)
		delete(sessions, s.token)
	}
	log.Printf("\t%s\tTimeout. Num sessions: %d\n", s.token, len(sessions))
}

func newsession(w http.ResponseWriter, r *http.Request) {
	token, err := util.GenerateRandomString(48)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "Error when generating random token")
		return
	}
	session := session{
		token:            token,
		upstreamCloser:   make(chan struct{}),
		downstreamCloser: make(chan struct{}),
		timeoutTimer:     time.NewTimer(time.Second * 60),
	}
	sessionsMut.Lock()
	sessions[token] = &session
	log.Printf("\t%s\tToken created. Num sessions: %d\n", token, len(sessions))
	sessionsMut.Unlock()

	// Timeout handler
	go sessionTimeoutHandler(&session)

	io.WriteString(w, token)
}

func up(w http.ResponseWriter, r *http.Request) {
	token := httprouter.GetParam(r, "token")
	defer r.Body.Close()
	defer log.Printf("\t%s\tClosing upstream connection\n", token)
	sessionsMut.Lock()
	session := sessions[token]
	if session == nil {
		sessionsMut.Unlock()
		log.Printf("\t%s\tSession does not exist\n", token)
		util.DropConnection(w)
		return
	}
	if !session.setUpstream(w, r) {
		sessionsMut.Unlock()
		log.Printf("\t%s\tCould not connect to upstream channel\n", token)
		util.DropConnection(w)
		return
	}
	sessionsMut.Unlock()
	log.Printf("\t%s\tWaiting on upstreamCloser\n", token)
	<-session.upstreamCloser
	if session.status == statusError {
		util.DropConnection(w)
		return
	}
}

func down(w http.ResponseWriter, r *http.Request) {
	token := httprouter.GetParam(r, "token")
	r.Body.Close()
	defer log.Printf("\t%s\tClosing downstream connection\n", token)
	sessionsMut.Lock()
	session := sessions[token]
	if session == nil {
		sessionsMut.Unlock()
		log.Printf("\t%s\tSession does not exist\n", token)
		util.DropConnection(w)
		return
	}
	if !session.setDownstream(w, r) {
		sessionsMut.Unlock()
		log.Printf("\t%s\tCould not connect to downstream channel\n", token)
		util.DropConnection(w)
		return
	}
	sessionsMut.Unlock()
	log.Printf("\t%s\tWaiting on downstreamCloser\n", token)
	<-session.downstreamCloser
	if session.status == statusError {
		util.DropConnection(w)
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
	router.PUT("/up/:token/*unused", up)
	router.GET("/down/:token", down)
	router.GET("/down/:token/*unused", down)

	log.Fatal(http.ListenAndServe(":8085", router))
}
