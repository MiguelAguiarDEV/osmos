package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// normalizeClipboard deja el texto en una forma canónica: saltos de línea LF
// y sin newline final.
//
// Es necesario para evitar un bucle de reenvíos entre plataformas: en Windows
// `Get-Clipboard` devuelve el contenido con un "\r\n" añadido, mientras que
// `wl-paste -n` en Linux quita el newline final. Sin normalizar, cada lado ve
// un texto distinto al que acaba de recibir, lo considera un cambio local y lo
// reenvía, indefinidamente.
func normalizeClipboard(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimRight(s, "\n")
}

// clipboardWriteBackend returns the backend name that will be used to write clipboard.
func clipboardWriteBackend() string {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("clip.exe"); err == nil {
			return "clip.exe"
		}
		return "powershell"
	}
	if _, err := exec.LookPath("wl-copy"); err == nil {
		return "wl-copy"
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return "xclip"
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		return "xsel"
	}
	return ""
}

// writeClipboardCmd ejecuta un backend de escritura pasándole el texto por
// stdin.
//
// stdout y stderr van a /dev/null a propósito: wl-copy, xclip y xsel se
// demonizan para mantener la selección, y el hijo hereda los descriptores.
// Si se usaran pipes (CombinedOutput/Output), Wait se quedaría esperando a que
// el demonio los cierre y la llamada colgaría indefinidamente.
func writeClipboardCmd(name string, args []string, s string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = bytes.NewBufferString(s)
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err == nil {
		defer devnull.Close()
		cmd.Stdout = devnull
		cmd.Stderr = devnull
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// readClipboardCmd usa Output() en vez de CombinedOutput() para que un aviso
// en stderr del backend no acabe pegado dentro del contenido del portapapeles.
func readClipboardCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}

func getClipboardText() (string, error) {
	if runtime.GOOS == "windows" {
		// Prefer Windows PowerShell; -Raw keeps text as-is.
		out, err := readClipboardCmd("powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw")
		if err != nil {
			return "", err
		}
		return normalizeClipboard(out), nil
	}
	// Se prueban los backends en orden y se recuerda el último error real:
	// decir "no hay backend" cuando xclip sí está instalado pero falló (por
	// ejemplo, con la selección vacía o sin DISPLAY) manda a depurar al sitio
	// equivocado.
	var lastErr error
	found := false
	for _, b := range [][]string{
		{"wl-paste", "-n"},
		{"xclip", "-selection", "clipboard", "-o"},
		{"xsel", "--clipboard", "--output"},
	} {
		if _, err := exec.LookPath(b[0]); err != nil {
			continue
		}
		found = true
		out, err := readClipboardCmd(b[0], b[1:]...)
		if err == nil {
			return normalizeClipboard(out), nil
		}
		lastErr = err
	}
	if !found {
		return "", errors.New("no clipboard backend found (install wl-clipboard or xclip)")
	}
	return "", lastErr
}

func setClipboardText(s string) error {
	if runtime.GOOS == "windows" {
		// Prefer clip.exe (simple, fast). Fallback to PowerShell Set-Clipboard.
		if _, err := exec.LookPath("clip.exe"); err == nil {
			return writeClipboardCmd("clip.exe", nil, s)
		}
		return writeClipboardCmd("powershell", []string{"-NoProfile", "-Command", "Set-Clipboard"}, s)
	}
	if _, err := exec.LookPath("wl-copy"); err == nil {
		return writeClipboardCmd("wl-copy", nil, s)
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return writeClipboardCmd("xclip", []string{"-selection", "clipboard"}, s)
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		return writeClipboardCmd("xsel", []string{"--clipboard", "--input"}, s)
	}
	return errors.New("no clipboard backend found (install wl-clipboard or xclip)")
}
