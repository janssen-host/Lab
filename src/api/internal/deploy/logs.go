package deploy

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/llllogan/lab-controller/internal/config"
)

var containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var logUpgrader = websocket.Upgrader{}

func (s *Service) StackLogs(w http.ResponseWriter, r *http.Request) {
	container := r.URL.Query().Get("container")
	if !containerName.MatchString(container) {
		http.Error(w, "container query parameter must be a container name or ID", http.StatusBadRequest)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "WebSocket upgrade required", http.StatusUpgradeRequired)
		return
	}
	var st stackRecord
	var port int
	err := s.db.QueryRowContext(r.Context(), `SELECT s.name,s.node,n.hostname,n.ssh_port FROM stacks s JOIN nodes n ON n.name=s.node WHERE s.id=?`, r.PathValue("id")).Scan(&st.Name, &st.Node, &st.Hostname, &port)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "stack not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream, err := s.remote.StreamLogs(ctx, config.Node{Name: st.Node, Hostname: st.Hostname, SSHPort: port}, st.Name, container)
	if err != nil {
		http.Error(w, "could not start log stream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer stream.Close()
	socket, err := logUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer socket.Close()
	socket.SetReadLimit(1024)
	go func() {
		defer cancel()
		for {
			if _, _, err := socket.ReadMessage(); err != nil {
				return
			}
		}
	}()
	type logChunk struct {
		data []byte
		err  error
	}
	chunks := make(chan logChunk, 128)
	go func() {
		reader := bufio.NewReaderSize(stream, 64*1024)
		for {
			line, err := reader.ReadSlice('\n')
			chunk := logChunk{data: append([]byte(nil), line...), err: err}
			select {
			case chunks <- chunk:
			case <-ctx.Done():
				return
			}
			if err != nil && err != bufio.ErrBufferFull {
				return
			}
		}
	}()
	var buffer strings.Builder
	var timer *time.Timer
	var timerC <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	flush := func() error {
		if buffer.Len() == 0 {
			return nil
		}
		text := strings.ToValidUTF8(buffer.String(), "�")
		buffer.Reset()
		_ = socket.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return socket.WriteMessage(websocket.TextMessage, []byte(text))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timerC:
			timerC = nil
			if flush() != nil {
				return
			}
		case chunk := <-chunks:
			if len(chunk.data) > 0 {
				buffer.Write(chunk.data)
				if timerC == nil {
					if timer == nil {
						timer = time.NewTimer(100 * time.Millisecond)
					} else {
						timer.Reset(100 * time.Millisecond)
					}
					timerC = timer.C
				}
				if buffer.Len() >= 1024*1024 {
					if flush() != nil {
						return
					}
				}
			}
			if chunk.err != nil && chunk.err != bufio.ErrBufferFull {
				if flush() != nil {
					return
				}
				if !errors.Is(chunk.err, io.EOF) && ctx.Err() == nil {
					_ = socket.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "log stream failed"), time.Now().Add(time.Second))
				} else {
					_ = socket.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				}
				return
			}
		}
	}
}
