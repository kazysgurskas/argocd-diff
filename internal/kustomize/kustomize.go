// Package kustomize builds kustomizations with the kustomize library, as
// `kustomize build` would, with ArgoCD's kustomize settings applied first.
package kustomize

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/kustomize/kyaml/resid"
	"sigs.k8s.io/yaml"

	"github.com/kazysgurskas/argocd-diff/internal/application"
)

// BuildOptions tune a build: the `kustomize build` flags that ArgoCD's
// kustomize.buildOptions setting may carry and that an in-process build
// can honor, and the Kubernetes version charts inflate for. The zero value
// is `kustomize build` with its defaults.
type BuildOptions struct {
	// LoadRestrictions says where a kustomization may load files from. The
	// zero value means from below its own directory only.
	LoadRestrictions types.LoadRestrictions
	// EnableHelm lets helmCharts entries inflate charts, which runs the
	// HelmCommand binary, helm when empty.
	EnableHelm  bool
	HelmCommand string
	// KubeVersion is the Kubernetes version helmCharts entries inflate for
	// when the source's kustomize block sets none, as ArgoCD passes the
	// cluster's. Empty leaves it to helm.
	KubeVersion string
}

// ParseBuildOptions reads kustomize.buildOptions word by word, as ArgoCD
// passes it to `kustomize build`. It accepts --enable-helm, --helm-command
// <binary> and --load-restrictor LoadRestrictionsNone or
// LoadRestrictionsRootOnly, with the value in the next word or after an
// equals sign. Any other flag is refused, since no kustomize binary runs
// here to take it.
func ParseBuildOptions(s string) (BuildOptions, error) {
	var opts BuildOptions
	words := strings.Fields(s)
	for i := 0; i < len(words); i++ {
		flag, value, inline := strings.Cut(words[i], "=")
		if !inline && flag != "--enable-helm" {
			if i+1 >= len(words) {
				return opts, fmt.Errorf("kustomize build option %s needs a value", flag)
			}
			i++
			value = words[i]
		}
		switch flag {
		case "--enable-helm":
			opts.EnableHelm = true
		case "--helm-command":
			opts.HelmCommand = value
		case "--load-restrictor":
			switch value {
			case "LoadRestrictionsNone":
				opts.LoadRestrictions = types.LoadRestrictionsNone
			case "LoadRestrictionsRootOnly":
				opts.LoadRestrictions = types.LoadRestrictionsRootOnly
			default:
				return opts, fmt.Errorf("kustomize build option --load-restrictor %q is neither LoadRestrictionsNone nor LoadRestrictionsRootOnly", value)
			}
		default:
			return opts, fmt.Errorf("kustomize build option %s is not supported; --enable-helm, --helm-command and --load-restrictor are", flag)
		}
	}
	return opts, nil
}

// ErrConflict says that a label or annotation of the Application's
// kustomize block is already set in the kustomization, which ArgoCD
// refuses unless the force setting for it is on.
var ErrConflict = errors.New("already set in the kustomization")

// IsKustomization reports whether dir holds a kustomization file, which is
// how ArgoCD detects a Kustomize source.
func IsKustomization(dir string) bool {
	return kustomizationFile(filesys.MakeFsOnDisk(), dir) != ""
}

// Render builds the kustomization at dir and returns the manifest stream.
//
// The build is `kustomize build` with its defaults, changed only by build:
// files load from below dir only, and neither plugins nor Helm chart
// inflation run. settings, the source's kustomize block, is applied to the
// kustomization first, the way ArgoCD applies it with `kustomize edit`. The
// edit happens in memory, so the file on disk is left as it is. A label or
// annotation the kustomization already sets is refused with ErrConflict
// unless the force setting for it is on. Settings that only a kustomize
// binary or ArgoCD's build environment could act on are logged as warnings
// and otherwise ignored.
func Render(dir string, settings *application.KustomizeSource, build BuildOptions) (string, error) {
	warnUnsupported(dir, settings)
	disk := filesys.MakeFsOnDisk()
	fs, err := edited(disk, dir, settings)
	if err != nil {
		return "", err
	}
	options := krusty.MakeDefaultOptions()
	if build.LoadRestrictions != types.LoadRestrictionsUnknown {
		options.LoadRestrictions = build.LoadRestrictions
	}
	if build.EnableHelm {
		options.PluginConfig.HelmConfig = types.HelmConfig{Enabled: true, Command: build.HelmCommand}
		if options.PluginConfig.HelmConfig.Command == "" {
			options.PluginConfig.HelmConfig.Command = "helm"
		}
		options.PluginConfig.HelmConfig.KubeVersion = build.KubeVersion
		if settings != nil {
			if settings.KubeVersion != "" {
				options.PluginConfig.HelmConfig.KubeVersion = settings.KubeVersion
			}
			options.PluginConfig.HelmConfig.ApiVersions = settings.APIVersions
		}
	}
	slog.Info("building kustomization", "path", dir)
	resources, err := krusty.MakeKustomizer(options).Run(fs, dir)
	if err != nil {
		return "", fmt.Errorf("build kustomization %s: %w", dir, err)
	}
	out, err := resources.AsYaml()
	if err != nil {
		return "", fmt.Errorf("build kustomization %s: %w", dir, err)
	}
	return string(out), nil
}

// warnUnsupported logs the settings ArgoCD would act on that the build here
// cannot: a kustomize version, since the library's own version builds, and
// build environment variables such as ${ARGOCD_APP_NAME}, which ArgoCD
// substitutes in images and labels, and in annotations when
// commonAnnotationsEnvsubst is set.
func warnUnsupported(dir string, s *application.KustomizeSource) {
	if s == nil {
		return
	}
	if s.Version != "" {
		slog.Warn("ignoring kustomize version, building with the library's own", "path", dir, "version", s.Version)
	}
	if s.CommonAnnotationsEnvsubst {
		slog.Warn("commonAnnotationsEnvsubst is not supported, annotations are left as written", "path", dir)
	}
	for _, value := range append(slices.Sorted(maps.Values(s.CommonLabels)), s.Images...) {
		if strings.Contains(value, "${") {
			slog.Warn("build environment variables are not substituted, value is left as written", "path", dir, "value", value)
		}
	}
}

// edited returns fs with the kustomization in dir replaced, in memory, by
// one with settings applied. It returns fs as it is when settings changes
// nothing or when dir holds no kustomization; the build reports the latter.
func edited(fs filesys.FileSystem, dir string, settings *application.KustomizeSource) (filesys.FileSystem, error) {
	if !hasSettings(settings) {
		return fs, nil
	}
	path := kustomizationFile(fs, dir)
	if path == "" {
		return fs, nil
	}
	content, err := fs.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var kustomization types.Kustomization
	if err := kustomization.Unmarshal(content); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	kustomization.FixKustomization()
	if err := apply(&kustomization, fs, dir, settings); err != nil {
		return nil, fmt.Errorf("apply kustomize settings to %s: %w", path, err)
	}
	out, err := yaml.Marshal(&kustomization)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", path, err)
	}
	return overlay{FileSystem: fs, path: path, content: out}, nil
}

// hasSettings reports whether settings changes the kustomization.
func hasSettings(s *application.KustomizeSource) bool {
	return s != nil && (s.NamePrefix != "" || s.NameSuffix != "" || s.Namespace != "" ||
		len(s.Images) > 0 || len(s.CommonLabels) > 0 || len(s.CommonAnnotations) > 0 ||
		len(s.Replicas) > 0 || len(s.Patches) > 0 || len(s.Components) > 0)
}

// kustomizationFile is the cleaned absolute path of the kustomization file
// in dir, resolved the way kustomize resolves it, or empty when dir holds
// none.
func kustomizationFile(fs filesys.FileSystem, dir string) string {
	for _, name := range konfig.RecognizedKustomizationFileNames() {
		path := filepath.Join(dir, name)
		if !fs.Exists(path) || fs.IsDir(path) {
			continue
		}
		confirmed, file, err := fs.CleanedAbs(path)
		if err != nil {
			continue
		}
		return confirmed.Join(file)
	}
	return ""
}

// apply sets settings on the kustomization in dir the way ArgoCD's
// `kustomize edit` calls would. The name prefix, name suffix and namespace
// replace what is there. An image or replica entry replaces the entry of
// the same name. Labels and annotations are added, but a key the
// kustomization already sets is refused unless the force setting for it is
// on. Patches are appended. Components are appended once each, and with
// ignoreMissingComponents a component directory without a kustomization is
// left out.
func apply(k *types.Kustomization, fs filesys.FileSystem, dir string, settings *application.KustomizeSource) error {
	if settings.NamePrefix != "" {
		k.NamePrefix = settings.NamePrefix
	}
	if settings.NameSuffix != "" {
		k.NameSuffix = settings.NameSuffix
	}
	if settings.Namespace != "" {
		k.Namespace = settings.Namespace
	}
	for _, arg := range settings.Images {
		image, err := parseImage(arg)
		if err != nil {
			return err
		}
		k.Images = slices.DeleteFunc(k.Images, func(existing types.Image) bool { return existing.Name == image.Name })
		k.Images = append(k.Images, image)
	}
	for _, replica := range settings.Replicas {
		k.Replicas = slices.DeleteFunc(k.Replicas, func(existing types.Replica) bool { return existing.Name == replica.Name })
		k.Replicas = append(k.Replicas, types.Replica{Name: replica.Name, Count: replica.Count})
	}
	if len(settings.CommonLabels) > 0 {
		if err := refuseConflicts("label", existingLabels(k, settings), settings.CommonLabels, settings.ForceCommonLabels, "forceCommonLabels"); err != nil {
			return err
		}
		addLabels(k, settings)
	}
	if len(settings.CommonAnnotations) > 0 {
		if err := refuseConflicts("annotation", k.CommonAnnotations, settings.CommonAnnotations, settings.ForceCommonAnnotations, "forceCommonAnnotations"); err != nil {
			return err
		}
		if k.CommonAnnotations == nil {
			k.CommonAnnotations = map[string]string{}
		}
		maps.Copy(k.CommonAnnotations, settings.CommonAnnotations)
	}
	for _, patch := range settings.Patches {
		k.Patches = append(k.Patches, convertPatch(patch))
	}
	for _, component := range settings.Components {
		path := component
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if settings.IgnoreMissingComponents && !fs.Exists(path) {
			slog.Info("ignoring missing component", "path", dir, "component", component)
			continue
		}
		if !slices.Contains(k.Components, component) {
			k.Components = append(k.Components, component)
		}
	}
	return nil
}

// existingLabels are the labels the kustomization already sets in the place
// `kustomize edit add label` would write to: commonLabels, or the labels
// entry without selectors that matches the templates choice.
func existingLabels(k *types.Kustomization, settings *application.KustomizeSource) map[string]string {
	if !settings.LabelWithoutSelector {
		return k.CommonLabels //nolint:staticcheck // deprecated, but it is where `kustomize edit add label` looks for an existing key
	}
	if label := labelsWithoutSelectors(k, settings.LabelIncludeTemplates); label != nil {
		return label.Pairs
	}
	return nil
}

// labelsWithoutSelectors is the kustomization's labels entry that leaves
// selectors alone and matches includeTemplates, which is the entry
// `kustomize edit add label --without-selector` writes to, or nil.
func labelsWithoutSelectors(k *types.Kustomization, includeTemplates bool) *types.Label {
	for i := range k.Labels {
		if !k.Labels[i].IncludeSelectors && k.Labels[i].IncludeTemplates == includeTemplates {
			return &k.Labels[i]
		}
	}
	return nil
}

// addLabels writes the labels where `kustomize edit add label` writes them.
// Without selectors they go to the matching labels entry, or a new one.
// With selectors they go to commonLabels when the kustomization uses it,
// because kustomize applies commonLabels after every labels entry and a
// new entry could not override a key set there; a kustomization without
// commonLabels gets a labels entry with includeSelectors, which means the
// same without the deprecated field.
func addLabels(k *types.Kustomization, settings *application.KustomizeSource) {
	switch {
	case settings.LabelWithoutSelector:
		label := labelsWithoutSelectors(k, settings.LabelIncludeTemplates)
		if label == nil {
			k.Labels = append(k.Labels, types.Label{IncludeTemplates: settings.LabelIncludeTemplates})
			label = &k.Labels[len(k.Labels)-1]
		}
		if label.Pairs == nil {
			label.Pairs = map[string]string{}
		}
		maps.Copy(label.Pairs, settings.CommonLabels)
	case len(k.CommonLabels) > 0: //nolint:staticcheck // deprecated, but what the kustomization uses must be overridden in place
		maps.Copy(k.CommonLabels, settings.CommonLabels) //nolint:staticcheck // same
	default:
		k.Labels = append(k.Labels, types.Label{Pairs: maps.Clone(settings.CommonLabels), IncludeSelectors: true})
	}
}

// refuseConflicts refuses a key of added that existing already sets, as
// `kustomize edit add` does without --force, unless forced. what names the
// kind of key in the error and force the ArgoCD setting that allows the
// override.
func refuseConflicts(what string, existing, added map[string]string, forced bool, force string) error {
	if forced {
		return nil
	}
	for _, key := range slices.Sorted(maps.Keys(added)) {
		if _, taken := existing[key]; taken {
			return fmt.Errorf("%s %s is %w; set %s to override it", what, key, ErrConflict, force)
		}
	}
	return nil
}

// convertPatch turns ArgoCD's patch into kustomize's, which has the same
// shape.
func convertPatch(patch application.KustomizePatch) types.Patch {
	converted := types.Patch{Path: patch.Path, Patch: patch.Patch}
	if target := patch.Target; target != nil {
		converted.Target = &types.Selector{
			ResId: resid.ResId{
				Gvk:       resid.Gvk{Group: target.Group, Version: target.Version, Kind: target.Kind},
				Name:      target.Name,
				Namespace: target.Namespace,
			},
			LabelSelector:      target.LabelSelector,
			AnnotationSelector: target.AnnotationSelector,
		}
	}
	if len(patch.Options) > 0 {
		converted.Options = &types.PatchArgs{AllowNameChange: patch.Options["allowNameChange"], AllowKindChange: patch.Options["allowKindChange"]}
	}
	return converted
}

// parseImage reads an image override as `kustomize edit set image` does:
// name=newName:newTag, name=newName@digest, name:newTag, name@digest or
// name=newName, where a name may carry a registry port.
func parseImage(arg string) (types.Image, error) {
	name, value, hasNewName := strings.Cut(arg, "=")
	if !hasNewName {
		value = name
	}
	var image types.Image
	target := value
	if before, digest, found := strings.Cut(value, "@"); found {
		target, image.Digest = before, digest
	} else if i := strings.LastIndex(value, ":"); i >= 0 && !strings.Contains(value[i:], "/") {
		target, image.NewTag = value[:i], value[i+1:]
	}
	if hasNewName {
		image.Name, image.NewName = name, target
	} else {
		image.Name = target
	}
	if image.Name == "" {
		return types.Image{}, fmt.Errorf("image override %q names no image", arg)
	}
	return image, nil
}

// overlay is a file system with the content of one file replaced.
type overlay struct {
	filesys.FileSystem
	path    string
	content []byte
}

// ReadFile returns the replaced content for the overlaid file and reads
// every other file from the underlying file system.
func (o overlay) ReadFile(path string) ([]byte, error) {
	if path == o.path {
		return o.content, nil
	}
	return o.FileSystem.ReadFile(path)
}
