package services

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
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
	// Service is the sw_service key the descriptor names, lowercase. Empty for redgreen's own.
	Service string `json:"service,omitempty"`
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

// DescribedIcon reads the service and logo another repository's orrery.yaml
// names. Only service and icon are read. Nil when it names no logo; an error
// when it names one the wrong way.
func DescribedIcon(text string) (service string, ref *IconRef, err error) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return "", nil, err
	}
	raw, ok := doc["icon"]
	if !ok || raw == nil {
		return "", nil, nil
	}
	service, _ = doc["service"].(string)
	if service == "" {
		return "", nil, errors.New("orrery.yaml names an icon but no service")
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "", nil, errors.New("orrery.yaml: icon needs a name and a file")
	}
	name, _ := m["name"].(string)
	file, _ := m["file"].(string)
	if name == "" || file == "" {
		return "", nil, errors.New("orrery.yaml: icon needs a name and a file")
	}
	return service, &IconRef{Name: name, File: file}, nil
}
