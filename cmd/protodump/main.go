package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/arkadiyt/protodump/pkg/protodump"
)

var debug bool

func Debug(str string, a ...any) (int, error) {
	if debug {
		return fmt.Printf(str, a...)
	}
	return 0, nil
}

func writeFile(outputDir string, filename string, content []byte) (string, error) {
	outputDirAbs, err := filepath.Abs(outputDir)
	if err != nil {
		return "", fmt.Errorf("couldn't get absolute dir for %s: %v", outputDir, err)
	}

	fileDir, fileBase := filepath.Split(filename)

	parts := strings.Split(path.Clean(fileDir), string(filepath.Separator))
	var i int
	for i = 0; i < len(parts); i++ {
		_, err := os.Stat(filepath.Join(outputDirAbs, filepath.Join(parts[:i+1]...)))
		if os.IsNotExist(err) {
			break
		}
	}

	eval := filepath.Join(outputDirAbs, filepath.Join(parts[:i]...))
	base, err := filepath.EvalSymlinks(eval)
	if err != nil {
		return "", fmt.Errorf("failed to evalsymlinks on %s: %v", eval, err)
	}

	if base != outputDirAbs && !strings.HasPrefix(base, outputDirAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid filepath: %s", base)
	}

	rest := filepath.Join(parts[i:]...)
	err = os.MkdirAll(filepath.Join(base, rest), 0700)
	if err != nil {
		return "", fmt.Errorf("failed to mkdirall on %s: %v", rest, err)
	}

	final := filepath.Join(base, rest, fileBase)

	file, err := os.OpenFile(final, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to open file %s: %v", final, err)
	}
	defer file.Close()
	_, err = file.Write(content)
	if err != nil {
		return "", fmt.Errorf("failed to write file %s: %v", final, err)
	}

	return final, nil
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Couldn't determine current working directory: %v\n", err)
	}

	var file = flag.String("file", "", "The file to extract definitions from")
	var output = flag.String("output", cwd, "The output directory to save definitions in (will be created if it doesn't exist). Defaults to current directory.")
	flag.BoolVar(&debug, "v", false, "Verbose output")
	var requiredFields = flag.Int("requiredFields", 3, "Minimum number of successfully parsed fields to be considered a valid FileDescriptorProto.")
	var pruneTarget = flag.String("prune", "", "Comma-separated list of target messages/services to keep. If provided, all other messages and files not in their dependency graph will be pruned.")
	var excludeStr string
	flag.StringVar(&excludeStr, "exclude", "", "Comma-separated list of packages to exclude from the returned protos.")
	flag.StringVar(&excludeStr, "excludePackages", "", "Comma-separated list of packages to exclude from the returned protos.")
	flag.StringVar(&excludeStr, "exclude-packages", "", "Comma-separated list of packages to exclude from the returned protos.")
	var goModule = flag.String("go-module", "", "Go module path prefix for generated go_package options")
	flag.Parse()

	if *file == "" {
		fmt.Printf("Usage:\n")
		flag.PrintDefaults()
		return
	}

	results, err := protodump.ScanFile(*file, *requiredFields)
	if err != nil {
		log.Fatalf("Got error scanning: %v\n", err)
	}

	err = os.MkdirAll(*output, 0700)
	if err != nil {
		log.Fatalf("Failed to create output folder %s: %v\n", *output, err)
	}

	var definitions []*protodump.ProtoDefinition
	for _, result := range results {
		definition, err := protodump.NewFromBytes(result)
		if err != nil {
			Debug("Got error parsing definition: %v\n", err)
		} else {
			definitions = append(definitions, definition)
		}
	}

	if *pruneTarget != "" {
		targets := strings.Split(*pruneTarget, ",")
		for i := range targets {
			targets[i] = strings.TrimSpace(targets[i])
		}

		definitions, err = protodump.PruneDefinitions(definitions, targets)
		if err != nil {
			log.Fatalf("Got error pruning definitions: %v\n", err)
		}
	}

	if excludeStr != "" {
		pkgs := strings.Split(excludeStr, ",")
		definitions = protodump.ExcludePackages(definitions, pkgs)
	}

	definitions, err = protodump.ResolveGoPackageCycles(definitions, *goModule)
	if err != nil {
		log.Fatalf("Got error resolving Go package cycles: %v\n", err)
	}

	for _, definition := range definitions {
		filename := definition.Filename()
		if strings.HasSuffix(filename, ".proto") {
			str, err := definition.String()
			if err != nil {
				fmt.Printf("Failed to format %s: %v\n", filename, err)
				continue
			}
			final, err := writeFile(*output, filename, []byte(str))
			if err != nil {
				fmt.Printf("Failed to write %s: %v\n", final, err)
			} else {
				fmt.Printf("Wrote %s\n", final)
			}
		} else {
			// Need to investigate further
		}
	}
}
