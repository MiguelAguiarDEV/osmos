package main

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
)

// clipboardReadBackend returns the backend that will be used to read the
// clipboard, or "" if none is available.
func clipboardReadBackend() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("pbpaste"); err == nil {
			return "pbpaste"
		}
		return ""
	}
	if _, err := exec.LookPath("wl-paste"); err == nil {
		return "wl-paste"
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return "xclip"
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		return "xsel"
	}
	return ""
}

// clipboardWriteBackend returns the backend name that will be used to write clipboard.
func clipboardWriteBackend() string {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("clip.exe"); err == nil {
			return "clip.exe"
		}
		return "powershell"
	}
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("pbcopy"); err == nil {
			return "pbcopy"
		}
		return ""
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

func getClipboardText() (string, error) {
	if runtime.GOOS == "windows" {
		// Prefer Windows PowerShell; -Raw keeps text as-is.
		cmd := exec.Command("powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("Get-Clipboard: %v", err)
		}
		return string(out), nil
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("pbpaste").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("pbpaste: %v", err)
		}
		return string(out), nil
	}
	// Wayland wl-paste
	if _, err := exec.LookPath("wl-paste"); err == nil {
		cmd := exec.Command("wl-paste", "-n")
		out, err := cmd.CombinedOutput()
		if err == nil {
			return string(out), nil
		}
	}
	// X11 xclip
	if _, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command("xclip", "-selection", "clipboard", "-o")
		out, err := cmd.CombinedOutput()
		if err == nil {
			return string(out), nil
		}
	}
	// X11 xsel
	if _, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.Command("xsel", "--clipboard", "--output")
		out, err := cmd.CombinedOutput()
		if err == nil {
			return string(out), nil
		}
	}
	return "", errors.New("no clipboard backend found (install wl-clipboard or xclip)")
}

func setClipboardText(s string) error {
	if runtime.GOOS == "windows" {
		// Prefer clip.exe (simple, fast). Fallback to PowerShell Set-Clipboard.
		if _, err := exec.LookPath("clip.exe"); err == nil {
			cmd := exec.Command("clip.exe")
			cmd.Stdin = bytes.NewBufferString(s)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("clip.exe: %v (%s)", err, string(out))
			}
			return nil
		}
		cmd := exec.Command("powershell", "-NoProfile", "-Command", "Set-Clipboard")
		cmd.Stdin = bytes.NewBufferString(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("Set-Clipboard: %v (%s)", err, string(out))
		}
		return nil
	}
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("pbcopy")
		cmd.Stdin = bytes.NewBufferString(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("pbcopy: %v (%s)", err, string(out))
		}
		return nil
	}
	if _, err := exec.LookPath("wl-copy"); err == nil {
		cmd := exec.Command("wl-copy")
		cmd.Stdin = bytes.NewBufferString(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("wl-copy: %v (%s)", err, string(out))
		}
		return nil
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command("xclip", "-selection", "clipboard")
		cmd.Stdin = bytes.NewBufferString(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("xclip: %v (%s)", err, string(out))
		}
		return nil
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.Command("xsel", "--clipboard", "--input")
		cmd.Stdin = bytes.NewBufferString(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("xsel: %v (%s)", err, string(out))
		}
		return nil
	}
	return errors.New("no clipboard backend found (install wl-clipboard or xclip)")
}
