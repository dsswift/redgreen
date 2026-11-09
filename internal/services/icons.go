package services

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// IconName is a custom icon name the hub takes.
var IconName = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// MaxIconBytes is the largest icon the hub takes.
const MaxIconBytes = 256 * 1024

var iconTypes = map[string]string{"svg": "image/svg+xml", "png": "image/png", "webp": "image/webp"}

// IconRef is what an orrery.yaml says about its logo.
type IconRef struct {
	Name string `yaml:"name" json:"name"`
	File string `yaml:"file" json:"file"`
}

// Icon is a logo ready to put on the hub.
type Icon struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	SHA256 string `json:"sha256"`
	Data   []byte `json:"data"`
}

// IconType is the type a file is sent as, from its extension, or "".
func IconType(file string) string {
	ext := strings.TrimPrefix(path.Ext(file), ".")
	if ext == "" {
		return ""
	}
	return iconTypes[strings.ToLower(ext)]
}

// CheckIcon checks a logo the way the hub does and returns it ready to put.
func CheckIcon(ref IconRef, data []byte) (*Icon, error) {
	if !IconName.MatchString(ref.Name) {
		return nil, fmt.Errorf("icon name %q does not match %s", ref.Name, IconName)
	}
	t := IconType(ref.File)
	if t == "" {
		return nil, fmt.Errorf("icon file %q is not .svg, .png, or .webp", ref.File)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("icon file %q is empty", ref.File)
	}
	if len(data) > MaxIconBytes {
		return nil, fmt.Errorf("icon file %q is %d bytes, over %d", ref.File, len(data), MaxIconBytes)
	}
	sum := sha256.Sum256(data)
	return &Icon{Name: ref.Name, Type: t, SHA256: hex.EncodeToString(sum[:]), Data: data}, nil
}
