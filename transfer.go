package main

import (
	"context"
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
	statusWaiting      transferStatus = iota
	statusTimedOut                    // One or both of the clients were too slow to connect
	statusAborted                     // One client connects and then closes the connection
	statusReady                       // Both clients have connected
	statusTransferring                // We start the transfer
	statusCompleted                   // The transfer completed successfully
	statusError                       // The transfer stopped halfway for some reason
)

type session struct {
	mu sync.Mutex

	token  string
	status transferStatus

	// An empty struct should be sent in after setting either upstream or downstream connects
	newConn chan struct{}
	// This channel is closed when the serve goroutine is completed or aborted so the client connections know to complete/abort
	done chan struct{}

	timeoutTimer *time.Timer

	upstreamCtx   context.Context
	downstreamCtx context.Context
	upstream      io.Reader
	downstream    io.Writer
}

var sessions struct {
	mu sync.RWMutex
	s  map[string]*session
}

// Creates and returns a new session, adding it to the sessions map as well
func newSession(token string) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	session := &session{
		token:        token,
		newConn:      make(chan struct{}),
		done:         make(chan struct{}),
		timeoutTimer: time.NewTimer(time.Second * 60),
	}
	sessions.s[token] = session
	go session.serve()
}

// Removes the session from the sessions map
// Should only be called from the session.serve func
func removeSession(s *session) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	delete(sessions.s, s.token)
}

func (s *session) Lock() {
	s.mu.Lock()
}
func (s *session) Unlock() {
	s.mu.Unlock()
}

// Gets the Done() of the upstream context if it exists
func (s *session) upstreamDone() <-chan struct{} {
	if s.upstreamCtx != nil {
		return s.upstreamCtx.Done()
	} else {
		return nil
	}
}

// Gets the Done() of the downstream context if it exists
func (s *session) downstreamDone() <-chan struct{} {
	if s.downstreamCtx != nil {
		return s.downstreamCtx.Done()
	} else {
		return nil
	}
}

// Returns true if both upstream and downstream have connected.
func (s *session) bothConnected() bool {
	s.Lock()
	defer s.Unlock()
	return s.upstream != nil && s.downstream != nil
}

// Handles an incoming upstream connection.
func (s *session) upstreamConnect(ctx context.Context, r io.Reader) {
	s.mu.Lock()
	if s.upstream == nil {
		log.Printf("\t%s\tConnecting to upstream", s.token)
		s.upstreamCtx = ctx
		s.upstream = r
		s.newConn <- struct{}{}
		s.mu.Unlock()
		// Wait until the transfer is done before returning
		<-s.done
	} else {
		// Another connection was already made to this session's upstream
		// Panic to abort the connection
		log.Printf("\t%s\tAlready connected", s.token)
		s.mu.Unlock()
		panic(http.ErrAbortHandler)
	}
}

// Handles an incoming downstream connection.
func (s *session) downstreamConnect(ctx context.Context, w io.Writer) {
	s.mu.Lock()
	if s.downstream == nil {
		log.Printf("\t%s\tConnecting to downstream", s.token)
		s.downstreamCtx = ctx
		s.downstream = w
		s.newConn <- struct{}{}
		s.mu.Unlock()
		// Wait until the transfer is done before returning
		<-s.done
	} else {
		// Another connection was already made to this session's upstream
		// Panic to abort the connection
		log.Printf("\t%s\tAlready connected", s.token)
		s.mu.Unlock()
		panic(http.ErrAbortHandler)
	}
}

// A goroutine that lasts the entire lifetime of a session.
// Main job is to handle timeouts/connections/aborts properly
func (s *session) serve() {
loop:
	for {
		select {
		case <-s.done:
			log.Printf("\t%s\tdone channel closed", s.token)
			removeSession(s)
			return
		case <-s.timeoutTimer.C:
			log.Printf("\t%s\ttimeout", s.token)
			// Timeout
			s.status = statusTimedOut
			close(s.done)
		case <-s.upstreamDone():
			log.Printf("\t%s\tupstream aborted", s.token)
			// Aborted by client
			s.status = statusAborted
			s.upstream = nil
			close(s.done)
		case <-s.downstreamDone():
			log.Printf("\t%s\tdownstream aborted", s.token)
			// Aborted by client
			s.status = statusAborted
			s.downstream = nil
			close(s.done)
		case <-s.newConn:
			log.Printf("\t%s\tnew connection", s.token)
			// New connection
			if s.bothConnected() {
				// Both are connected, break to move to meat of the function
				break loop
			} else {
				// First client connected, wait for the other client
				util.RefreshTimer(s.timeoutTimer, time.Second*300)
				continue
			}
		}
	}
	log.Printf("\t%s\tcopying", s.token)
	// Both upstream and downstream have connected. Let's roll!
	io.Copy(s.downstream, s.upstream)
	removeSession(s)
	close(s.done)
}

// Handlers

func indexHandler(w http.ResponseWriter, r *http.Request) {
	log.Println(r.URL)
	http.ServeFile(w, r, "index.html")
}

func newsessionHandler(w http.ResponseWriter, r *http.Request) {
	log.Println(r.URL)
	token, err := util.GenerateRandomString(48)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "Error when generating random token")
		return
	}
	newSession(token)
	io.WriteString(w, token)
}

func upHandler(w http.ResponseWriter, r *http.Request) {
	log.Println(r.URL)
	token := httprouter.GetParam(r, "token")
	defer r.Body.Close()
	defer log.Printf("\t%s\tClosing upstream connection\n", token)
	sessions.mu.Lock()
	session := sessions.s[token]
	sessions.mu.Unlock()
	if session == nil {
		log.Printf("\t%s\tSession does not exist\n", token)
		util.DropConnection(w)
		return
	}
	session.upstreamConnect(r.Context(), r.Body)
}

func downHandler(w http.ResponseWriter, r *http.Request) {
	log.Println(r.URL)
	token := httprouter.GetParam(r, "token")
	defer r.Body.Close()
	defer log.Printf("\t%s\tClosing downstream connection\n", token)
	sessions.mu.Lock()
	session := sessions.s[token]
	sessions.mu.Unlock()
	if session == nil {
		log.Printf("\t%s\tSession does not exist\n", token)
		util.DropConnection(w)
		return
	}
	session.downstreamConnect(r.Context(), w)
}

func main() {
	sessions.mu.Lock()
	sessions.s = make(map[string]*session)
	sessions.mu.Unlock()

	router := httprouter.New()
	router.GET("/", indexHandler)
	router.GET("/newsession", newsessionHandler)
	router.PUT("/up/:token", upHandler)
	router.PUT("/up/:token/*unused", upHandler)
	router.GET("/down/:token", downHandler)
	router.GET("/down/:token/*unused", downHandler)

	log.Fatal(http.ListenAndServe(":8085", router))
}
