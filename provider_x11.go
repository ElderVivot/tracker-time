// provider_x11.go — Provedores de idle e janela ativa para X11.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// --- Idle (xprintidle) ---

// X11IdleProvider usa o comando xprintidle para obter o tempo ocioso em X11.
type X11IdleProvider struct{}

// IdleDuration retorna o tempo desde a última atividade de mouse/teclado (via xprintidle).
// Se xprintidle não estiver instalado ou falhar, retorna 0.
func (X11IdleProvider) IdleDuration() (time.Duration, error) {
	out, err := exec.Command("xprintidle").Output()
	if err != nil {
		return 0, nil
	}
	ms, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return time.Duration(ms) * time.Millisecond, nil
}

// --- Janela ativa (xgb) ---

// X11WindowProvider obtém a janela ativa via X11 (xgb).
// Deve ser fechado após o uso (Close) para liberar a conexão com o servidor X.
type X11WindowProvider struct {
	conn        *xgb.Conn
	root        xproto.Window
	activeAtom  xproto.Atom
	nameAtom    xproto.Atom
	classAtom   xproto.Atom
}

// parseDisplayNum extrai o número do display de uma string DISPLAY.
// Ex.: ":0" → 0, ":1" → 1, ":10.0" → 10. Retorna -1 se inválido.
func parseDisplayNum(display string) int {
	d := strings.TrimPrefix(display, ":")
	if i := strings.IndexByte(d, '.'); i >= 0 {
		d = d[:i]
	}
	n, err := strconv.Atoi(d)
	if err != nil {
		return -1
	}
	return n
}

// detectX11Env garante que DISPLAY (e XAUTHORITY) estejam definidos.
// Se a variável já existir no ambiente, não faz nada.
// Caso contrário, varre /proc buscando processos do mesmo UID que já
// possuam DISPLAY e escolhe o de menor número — sessões locais são
// tipicamente :0 ou :1, enquanto SSH X11 forwarding começa em :10.
func detectX11Env() {
	if os.Getenv("DISPLAY") != "" {
		return
	}
	uid := uint32(os.Getuid())
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}

	bestDisplay, bestXauth, bestPid := "", "", ""
	bestNum := -1

	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || len(name) == 0 || name[0] < '1' || name[0] > '9' {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uid {
			continue
		}
		data, err := os.ReadFile("/proc/" + name + "/environ")
		if err != nil {
			continue
		}
		var display, xauth string
		for _, kv := range bytes.Split(data, []byte{0}) {
			switch {
			case bytes.HasPrefix(kv, []byte("DISPLAY=")):
				display = string(kv[len("DISPLAY="):])
			case bytes.HasPrefix(kv, []byte("XAUTHORITY=")):
				xauth = string(kv[len("XAUTHORITY="):])
			}
		}
		if display == "" {
			continue
		}
		num := parseDisplayNum(display)
		if num < 0 {
			continue
		}
		if bestNum == -1 || num < bestNum {
			bestNum = num
			bestDisplay = display
			bestXauth = xauth
			bestPid = name
		}
	}

	if bestDisplay != "" {
		os.Setenv("DISPLAY", bestDisplay)
		if bestXauth != "" && os.Getenv("XAUTHORITY") == "" {
			os.Setenv("XAUTHORITY", bestXauth)
		}
		log.Printf("[x11] DISPLAY auto-detectado: %s (via /proc/%s)", bestDisplay, bestPid)
	}
}

// NewX11WindowProvider conecta ao servidor X e retorna um WindowProvider e uma função de cleanup.
// O cleanup deve ser chamado quando o monitor encerrar (ex.: defer cleanup()).
func NewX11WindowProvider() (WindowProvider, func(), error) {
	detectX11Env()
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, nil, err
	}
	setup := xproto.Setup(conn)
	root := setup.DefaultScreen(conn).Root

	activeAtom, _ := xproto.InternAtom(conn, true, uint16(len("_NET_ACTIVE_WINDOW")), "_NET_ACTIVE_WINDOW").Reply()
	nameAtom, _ := xproto.InternAtom(conn, true, uint16(len("_NET_WM_NAME")), "_NET_WM_NAME").Reply()
	classAtom, _ := xproto.InternAtom(conn, true, uint16(len("WM_CLASS")), "WM_CLASS").Reply()

	if activeAtom == nil || nameAtom == nil || classAtom == nil {
		conn.Close()
		return nil, nil, fmt.Errorf("x11: falha ao obter átomos")
	}

	p := &X11WindowProvider{
		conn:       conn,
		root:       root,
		activeAtom: activeAtom.Atom,
		nameAtom:   nameAtom.Atom,
		classAtom:  classAtom.Atom,
	}
	return p, func() { conn.Close() }, nil
}

// ActiveWindow retorna (processName, windowTitle) da janela ativa em X11.
// Retorna erro não-nil quando a conexão com o servidor X falha (sessão encerrada, etc.).
func (p *X11WindowProvider) ActiveWindow() (processName, windowTitle string, err error) {
	reply, err := xproto.GetProperty(p.conn, false, p.root, p.activeAtom, xproto.GetPropertyTypeAny, 0, 1<<32-1).Reply()
	if err != nil {
		return "", "", fmt.Errorf("x11: %w", err)
	}
	if reply == nil || len(reply.Value) < 4 {
		return "", "", nil
	}
	windowID := xproto.Window(binary.LittleEndian.Uint32(reply.Value))

	reply, _ = xproto.GetProperty(p.conn, false, windowID, p.nameAtom, xproto.GetPropertyTypeAny, 0, 1<<32-1).Reply()
	if reply != nil && len(reply.Value) > 0 {
		windowTitle = string(bytes.TrimRight(reply.Value, "\x00"))
	}

	reply, _ = xproto.GetProperty(p.conn, false, windowID, p.classAtom, xproto.GetPropertyTypeAny, 0, 1<<32-1).Reply()
	if reply != nil && len(reply.Value) > 0 {
		parts := strings.SplitN(string(reply.Value), "\x00", 3)
		if len(parts) >= 2 && parts[1] != "" {
			processName = parts[1]
		} else if len(parts) >= 1 && parts[0] != "" {
			processName = parts[0]
		}
	}
	if processName == "" {
		processName = "unknown"
	}
	return processName, windowTitle, nil
}
