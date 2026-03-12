// provider_wayland.go — Provedores de idle e janela ativa para Wayland (GNOME).
// Usa gdbus para comunicação D-Bus com a extensão tracker-time@autmais e org.gnome.Mutter.IdleMonitor.
//
// Requer a extensão GNOME Shell instalada e ativa (ver gnome-extension/install.sh).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// --- Idle (org.gnome.Mutter.IdleMonitor) ---

// WaylandIdleProvider usa D-Bus para obter o tempo ocioso no GNOME/Wayland.
type WaylandIdleProvider struct{}

// IdleDuration retorna o tempo desde a última atividade de mouse/teclado via Mutter IdleMonitor.
func (WaylandIdleProvider) IdleDuration() (time.Duration, error) {
	out, err := exec.Command(
		"gdbus", "call", "--session",
		"--dest", "org.gnome.Mutter.IdleMonitor",
		"--object-path", "/org/gnome/Mutter/IdleMonitor/Core",
		"--method", "org.gnome.Mutter.IdleMonitor.GetIdletime",
	).Output()
	if err != nil {
		return 0, fmt.Errorf("wayland idle: %w", err)
	}
	// Saída: "(uint64 1234,)\n" — extraímos o número.
	s := strings.TrimSpace(string(out))
	s = strings.TrimPrefix(s, "(uint64 ")
	s = strings.TrimSuffix(s, ",)")
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("wayland idle parse: %q: %w", string(out), err)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// --- Janela ativa (extensão GNOME Shell via D-Bus) ---

// WaylandWindowProvider obtém a janela ativa via extensão tracker-time@autmais D-Bus.
type WaylandWindowProvider struct{}

const (
	dbusExtDest   = "org.gnome.Shell.Extensions.TrackerTime"
	dbusExtPath   = "/org/gnome/Shell/Extensions/TrackerTime"
	dbusExtMethod = "org.gnome.Shell.Extensions.TrackerTime.GetActiveWindow"
)

// detectWaylandEnv verifica se estamos em sessão GNOME/Wayland e se DBUS_SESSION_BUS_ADDRESS
// está disponível. Se não estiver, tenta detectar via /proc (similar ao detectX11Env).
func detectWaylandEnv() error {
	if os.Getenv("XDG_SESSION_TYPE") != "wayland" {
		return fmt.Errorf("XDG_SESSION_TYPE não é wayland")
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return nil
	}
	if err := detectDBusFromProc(); err != nil {
		return fmt.Errorf("wayland: DBUS_SESSION_BUS_ADDRESS não encontrado: %w", err)
	}
	return nil
}

// detectDBusFromProc varre /proc buscando processos do mesmo UID que tenham
// DBUS_SESSION_BUS_ADDRESS definido (necessário quando rodando como serviço systemd).
func detectDBusFromProc() error {
	uid := uint32(os.Getuid())
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
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
		for _, kv := range splitNull(data) {
			if strings.HasPrefix(kv, "DBUS_SESSION_BUS_ADDRESS=") {
				addr := strings.TrimPrefix(kv, "DBUS_SESSION_BUS_ADDRESS=")
				os.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
				return nil
			}
		}
	}
	return fmt.Errorf("nenhum processo com DBUS_SESSION_BUS_ADDRESS encontrado")
}

// splitNull divide bytes por \x00 e retorna strings.
func splitNull(data []byte) []string {
	var result []string
	start := 0
	for i, b := range data {
		if b == 0 {
			if i > start {
				result = append(result, string(data[start:i]))
			}
			start = i + 1
		}
	}
	if start < len(data) {
		result = append(result, string(data[start:]))
	}
	return result
}

// NewWaylandWindowProvider verifica o ambiente Wayland/GNOME e testa se a extensão está ativa.
func NewWaylandWindowProvider() (WindowProvider, func(), error) {
	if err := detectWaylandEnv(); err != nil {
		return nil, nil, err
	}
	// Testa se a extensão responde via D-Bus.
	_, err := exec.Command(
		"gdbus", "call", "--session",
		"--dest", dbusExtDest,
		"--object-path", dbusExtPath,
		"--method", dbusExtMethod,
	).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("wayland: extensão tracker-time@autmais não respondeu via D-Bus (instale com gnome-extension/install.sh e ative com: gnome-extensions enable tracker-time@autmais): %w", err)
	}
	return &WaylandWindowProvider{}, func() {}, nil
}

// ActiveWindow retorna (processName, windowTitle) da janela ativa via extensão GNOME Shell.
func (p *WaylandWindowProvider) ActiveWindow() (processName, windowTitle string, err error) {
	out, err := exec.Command(
		"gdbus", "call", "--session",
		"--dest", dbusExtDest,
		"--object-path", dbusExtPath,
		"--method", dbusExtMethod,
	).Output()
	if err != nil {
		return "", "", fmt.Errorf("wayland window: %w", err)
	}
	// Saída do gdbus: ('firefox', 'Google - Mozilla Firefox')\n
	processName, windowTitle = parseDBusTwoStrings(string(out))
	if processName == "" && windowTitle == "" {
		return "", "", nil
	}
	if processName == "" {
		processName = "unknown"
	}
	return processName, windowTitle, nil
}

// parseDBusTwoStrings extrai dois valores string da resposta do gdbus.
// Input esperado: ('valor1', 'valor2')\n
func parseDBusTwoStrings(s string) (string, string) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")

	// Divide por "', '" preservando aspas simples internas.
	parts := strings.SplitN(s, "', '", 2)
	if len(parts) != 2 {
		return "", ""
	}
	v1 := strings.TrimPrefix(parts[0], "'")
	v2 := strings.TrimSuffix(parts[1], "'")
	return v1, v2
}
