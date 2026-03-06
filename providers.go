// providers.go — Interfaces para provedores de idle e janela ativa (X11).
package main

import "time"

// IdleProvider retorna o tempo que o usuário está ocioso (sem mouse/teclado).
type IdleProvider interface {
	IdleDuration() (time.Duration, error)
}

// WindowProvider retorna a janela atualmente ativa: nome do processo/app e título.
type WindowProvider interface {
	ActiveWindow() (processName, windowTitle string, err error)
}
