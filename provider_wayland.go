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
// está disponível. Se variáveis não estiverem no ambiente (ex: systemd user service),
// tenta detectar via /proc.
func detectWaylandEnv() error {
	// Tenta importar variáveis da sessão gráfica via /proc (necessário em systemd user services).
	_ = detectEnvFromProc([]string{
		"XDG_SESSION_TYPE",
		"XDG_CURRENT_DESKTOP",
		"DBUS_SESSION_BUS_ADDRESS",
		"WAYLAND_DISPLAY",
	})

	if os.Getenv("XDG_SESSION_TYPE") != "wayland" {
		return fmt.Errorf("XDG_SESSION_TYPE não é wayland (valor: %q)", os.Getenv("XDG_SESSION_TYPE"))
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		return fmt.Errorf("wayland: DBUS_SESSION_BUS_ADDRESS não encontrado")
	}
	return nil
}

// detectEnvFromProc varre /proc buscando um processo do mesmo UID que possua
// TODAS as variáveis solicitadas, e importa-as de uma vez. Isso garante que as
// variáveis venham da mesma sessão gráfica (ex: gnome-session-binary ou gnome-shell),
// evitando misturar DISPLAY de Xwayland com XDG_SESSION_TYPE de outro processo.
// Necessário quando rodando como serviço systemd (user service).
func detectEnvFromProc(vars []string) error {
	// Determina quais variáveis ainda precisam ser importadas.
	var needed []string
	for _, v := range vars {
		if os.Getenv(v) == "" {
			needed = append(needed, v)
		}
	}
	if len(needed) == 0 {
		return nil
	}

	uid := uint32(os.Getuid())
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}

	// Para cada processo, tenta encontrar um que tenha TODAS as variáveis necessárias.
	// Se não encontrar um processo perfeito, usa o que tiver mais variáveis.
	type candidate struct {
		env   map[string]string
		count int
	}
	var best candidate

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

		found := make(map[string]string)
		for _, kv := range splitNull(data) {
			for _, v := range needed {
				if strings.HasPrefix(kv, v+"=") {
					found[v] = strings.TrimPrefix(kv, v+"=")
				}
			}
		}

		if len(found) == len(needed) {
			// Processo perfeito: tem todas as variáveis.
			for k, v := range found {
				os.Setenv(k, v)
			}
			return nil
		}
		if len(found) > best.count {
			best = candidate{env: found, count: len(found)}
		}
	}

	// Nenhum processo teve tudo — usa o melhor candidato.
	if best.count > 0 {
		for k, v := range best.env {
			os.Setenv(k, v)
		}
		// Reporta quais ficaram faltando.
		var missing []string
		for _, v := range needed {
			if os.Getenv(v) == "" {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("variáveis não encontradas em /proc: %s", strings.Join(missing, ", "))
		}
		return nil
	}
	return fmt.Errorf("nenhum processo com variáveis de sessão encontrado em /proc")
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
