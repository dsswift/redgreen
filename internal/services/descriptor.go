package services

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// API is one interface a described service offers.
type API struct {
	ID          string `yaml:"id" json:"id"`
	Title       string `yaml:"title" json:"title"`
	Kind        string `yaml:"kind" json:"kind"`
	Description string `yaml:"description" json:"description,omitempty"`
	// Spec is where its definition is read: a path from the repository root, or a full URL.
	Spec string `yaml:"spec" json:"spec,omitempty"`
}

// Descriptor is a repository's orrery.yaml: the service it says it is, the
// APIs that service offers and calls, and its place in one estate's scheme.
type Descriptor struct {
	Service     string         `yaml:"service" json:"service"`
	Icon        *IconRef       `yaml:"icon" json:"icon,omitempty"`
	Fields      map[string]any `yaml:"fields" json:"fields,omitempty"`
	Relations   map[string]any `yaml:"relations" json:"relations,omitempty"`
	Provides    []API          `yaml:"provides" json:"provides,omitempty"`
	Consumes    []string       `yaml:"consumes" json:"consumes,omitempty"`
	WillConsume []string       `yaml:"will_consume" json:"willConsume,omitempty"`
	Deploys     *Deploys       `yaml:"deploys" json:"deploys,omitempty"`
}

// Deploys says how the service reaches its environments where the forge's
// own deployments do not say.
type Deploys struct {
	// Pipeline is the pipeline file, from the repository root, whose
	// successful runs on the default branch or a release tag are the
	// service's deployments. For a service whose forge records none.
	Pipeline string `yaml:"pipeline" json:"pipeline,omitempty"`
	// Environment is where those runs deploy, "production" when unset.
	Environment string `yaml:"environment" json:"environment,omitempty"`
	// ArgoCD are the sources of the Argo CD apps that deploy the service: a
	// repository's browser URL, with the folder its manifests sit in.
	ArgoCD []string `yaml:"argocd" json:"argocd,omitempty"`
}

// DefaultEnvironment is where a pipeline's runs deploy when the file does not say.
const DefaultEnvironment = "production"

// PipelineEnvironment is the environment a pipeline's runs deploy to.
func (d *Deploys) PipelineEnvironment() string {
	if d.Environment == "" {
		return DefaultEnvironment
	}
	return d.Environment
}

var (
	fieldName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// APIKinds are the kinds of interface the hub's API blueprint takes.
	APIKinds = []string{"rest", "graphql", "grpc", "event", "web", "mcp"}
)

// ParseDescriptor reads an orrery.yaml and refuses one the hub would refuse.
// scope is the key of the repository it was read from, with the folder it
// sits in: the service it names must be that key or under it, so no file
// describes a service of another repository or another folder.
func ParseDescriptor(text []byte, scope string) (*Descriptor, error) {
	var d Descriptor
	dec := yaml.NewDecoder(bytes.NewReader(text))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("orrery.yaml: %w", err)
	}
	d.Service = strings.ToLower(d.Service)
	if d.Service == "" {
		return nil, errors.New("orrery.yaml: service is required")
	}
	if d.Service != scope && !strings.HasPrefix(d.Service, scope+"/") {
		return nil, fmt.Errorf("orrery.yaml: service %q is not %q or a folder in it", d.Service, scope)
	}
	if d.Icon != nil {
		if !IconName.MatchString(d.Icon.Name) || d.Icon.File == "" {
			return nil, fmt.Errorf("orrery.yaml: icon needs a name matching %s and a file", IconName)
		}
		if IconType(d.Icon.File) == "" {
			return nil, fmt.Errorf("orrery.yaml: icon file %q is not .svg, .png, or .webp", d.Icon.File)
		}
	}
	for k := range d.Fields {
		if !fieldName.MatchString(k) {
			return nil, fmt.Errorf("orrery.yaml: field %q is not a field name", k)
		}
	}
	for k := range d.Relations {
		if !fieldName.MatchString(k) {
			return nil, fmt.Errorf("orrery.yaml: relation %q is not a relation name", k)
		}
	}
	if err := checkDeploys(d.Deploys); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i, a := range d.Provides {
		if a.ID == "" || a.Title == "" {
			return nil, fmt.Errorf("orrery.yaml: provides[%d] needs an id and a title", i)
		}
		if !slices.Contains(APIKinds, a.Kind) {
			return nil, fmt.Errorf("orrery.yaml: provides[%d] kind %q is not one of %v", i, a.Kind, APIKinds)
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("orrery.yaml: api %q is listed twice", a.ID)
		}
		if _, err := specPath(a.Spec); err != nil {
			return nil, fmt.Errorf("orrery.yaml: provides[%d] spec: %w", i, err)
		}
		seen[a.ID] = true
	}
	return &d, nil
}

// checkDeploys refuses a deploys block that names no file in the repository
// or a source that is no web address. A pipeline path is kept in its clean form.
func checkDeploys(d *Deploys) error {
	if d == nil {
		return nil
	}
	if d.Pipeline == "" && len(d.ArgoCD) == 0 {
		return errors.New("orrery.yaml: deploys needs a pipeline or argocd sources")
	}
	if d.Pipeline != "" {
		file, err := specPath(d.Pipeline)
		if err != nil || file == "" {
			return fmt.Errorf("orrery.yaml: deploys.pipeline %q is not a file in the repository", d.Pipeline)
		}
		d.Pipeline = file
	} else if d.Environment != "" {
		return errors.New("orrery.yaml: deploys.environment is the pipeline's; name the pipeline")
	}
	for i, src := range d.ArgoCD {
		if !strings.HasPrefix(src, "https://") && !strings.HasPrefix(src, "http://") {
			return fmt.Errorf("orrery.yaml: deploys.argocd[%d] %q is not a repository's web address", i, src)
		}
	}
	return nil
}

// specPath is a spec's file in the repository, "" for a full URL or none.
func specPath(spec string) (string, error) {
	if spec == "" || strings.HasPrefix(spec, "https://") || strings.HasPrefix(spec, "http://") {
		return "", nil
	}
	if strings.HasPrefix(spec, "/") || strings.Contains(spec, "://") {
		return "", fmt.Errorf("%q is neither a path in the repository nor a full URL", spec)
	}
	file := path.Clean(spec)
	if file == ".." || strings.HasPrefix(file, "../") {
		return "", fmt.Errorf("%q leaves the repository", spec)
	}
	return file, nil
}

// SpecURL is where an API's definition is read: a full URL as it is, or a
// path's link on the default branch of the repository at repoURL, in the
// forge's own form.
func SpecURL(spec, repoURL, forge string) any {
	file, err := specPath(spec)
	if err != nil || spec == "" {
		return nil
	}
	if file == "" {
		return spec
	}
	base := strings.TrimSuffix(repoURL, "/")
	switch forge {
	case "gitlab":
		return base + "/-/blob/HEAD/" + file
	case "azuredevops":
		return base + "?path=/" + file
	default:
		return base + "/blob/HEAD/" + file
	}
}
