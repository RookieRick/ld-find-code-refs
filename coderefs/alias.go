package coderefs

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iancoleman/strcase"
	"github.com/launchdarkly/ld-find-code-refs/internal/helpers"
	"github.com/launchdarkly/ld-find-code-refs/internal/validation"
	"github.com/launchdarkly/ld-find-code-refs/options"

	"github.com/bmatcuk/doublestar/v4"
)

// GenerateAliases returns a map of flag keys to aliases based on config.
func GenerateAliases(flags []string, aliases []options.Alias, dir string) (map[string][]string, error) {
	allFileContents, err := processFileContent(aliases, dir)
	if err != nil {
		return nil, err
	}

	ret := make(map[string][]string, len(flags))
	for _, flag := range flags {
		for _, a := range aliases {
			flagAliases, err := generateAlias(a, flag, dir, allFileContents)
			if err != nil {
				return nil, err
			}
			ret[flag] = append(ret[flag], flagAliases...)
		}
		ret[flag] = helpers.Dedupe(ret[flag])
	}
	return ret, nil
}

func globToAbsolutePaths(basepath string, pattern string) ([]string, error) {
	fsys := os.DirFS(basepath)

	matches, err := doublestar.Glob(fsys, pattern)
	if err != nil {
		return nil, fmt.Errorf("could not process path glob '%s'", filepath.Join(basepath, pattern))
	}

	updatedMatches := matches[:0]
	for _, match := range matches {
		updatedMatches = append(updatedMatches, strings.Join([]string{basepath, match}, "/"))
	}

	return updatedMatches, nil
}

func generateAlias(a options.Alias, flag, dir string, allFileContents map[string][]byte) ([]string, error) {
	ret := []string{}
	switch a.Type.Canonical() {
	case options.Literal:
		ret = a.Flags[flag]
	case options.CamelCase:
		ret = []string{strcase.ToLowerCamel(flag)}
	case options.PascalCase:
		ret = []string{strcase.ToCamel(flag)}
	case options.SnakeCase:
		ret = []string{strcase.ToSnake(flag)}
	case options.UpperSnakeCase:
		ret = []string{strcase.ToScreamingSnake(flag)}
	case options.KebabCase:
		ret = []string{strcase.ToKebab(flag)}
	case options.DotCase:
		ret = []string{strcase.ToDelimited(flag, '.')}
	case options.FilePattern:
		// Concatenate the contents of all files into a single byte array to be matched by specified patterns
		fileContents := []byte{}
		for _, path := range a.Paths {
			matches, err := globToAbsolutePaths(dir, path)
			if err != nil {
				return nil, fmt.Errorf("could not process path glob '%s'", filepath.Join(dir, path))
			}
			for _, match := range matches {
				pathFileContents := allFileContents[match]
				if len(pathFileContents) > 0 {
					fileContents = append(fileContents, pathFileContents...)
				}
			}
		}

		for _, p := range a.Patterns {
			pattern := regexp.MustCompile(strings.ReplaceAll(p, "FLAG_KEY", flag))
			results := pattern.FindAllStringSubmatch(string(fileContents), -1)
			for _, res := range results {
				if len(res) > 1 {
					ret = append(ret, res[1:]...)
				}
			}
		}
	case options.Command:
		ctx := context.Background()
		if a.Timeout != nil && *a.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, time.Now().Add(time.Second*time.Duration(*a.Timeout)))
			defer cancel()
		}
		tokens := strings.Split(*a.Command, " ")
		name := tokens[0]
		args := []string{}
		if len(tokens) > 1 {
			args = tokens[1:]
		}
		/* #nosec */
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdin = strings.NewReader(flag)
		cmd.Dir = dir
		stdout, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("failed to execute alias command: %w", err)
		}
		err = json.Unmarshal(stdout, &ret)
		if err != nil {
			return nil, fmt.Errorf("could not unmarshal json output of alias command: %w", err)
		}
	}

	return ret, nil
}

// processFileContent reads and stores the content of files specified by filePattern alias matchers to be matched for aliases
func processFileContent(aliases []options.Alias, dir string) (map[string][]byte, error) {
	allFileContents := map[string][]byte{}
	for idx, a := range aliases {
		if a.Type.Canonical() != options.FilePattern {
			continue
		}

		aliasId := strconv.Itoa(idx)
		if a.Name != "" {
			aliasId = a.Name
		}

		paths := []string{}
		for _, glob := range a.Paths {
			matches, err := globToAbsolutePaths(dir, glob)
			if err != nil {
				return nil, fmt.Errorf("filepattern '%s': could not process path glob '%s'", aliasId, filepath.Join(dir, glob))
			}

			paths = append(paths, matches...)
		}
		paths = helpers.Dedupe(paths)

		for _, path := range paths {
			_, pathAlreadyProcessed := allFileContents[path]
			if pathAlreadyProcessed {
				continue
			}

			if !validation.FileExists(path) {
				return nil, fmt.Errorf("filepattern '%s': could not find file at path '%s'", aliasId, path)
			}
			/* #nosec */
			data, err := ioutil.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("filepattern '%s': could not process file at path '%s': %v", aliasId, path, err)
			}
			allFileContents[path] = data
		}
	}
	return allFileContents, nil
}
