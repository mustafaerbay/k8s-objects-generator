package split

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/pkg/errors"
)

type Project struct {
	OutputDir           string
	GitRepo             string
	SwaggerTemplatesDir string
	Root                string
}

func NewProject(outputDir, gitRepo, swaggerTemplatesDir string) (Project, error) {
	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		return Project{}, errors.Wrapf(err, "cannot calculate absolute path of %s", outputDir)
	}

	root := filepath.Join(absOut, "src", gitRepo)

	return Project{
		OutputDir:           outputDir,
		GitRepo:             gitRepo,
		SwaggerTemplatesDir: swaggerTemplatesDir,
		Root:                root,
	}, nil
}

func (p *Project) Init() error {
	err := os.RemoveAll(p.Root)
	if err != nil && !os.IsNotExist(err) {
		return errors.Wrapf(err, "cannot cleanup dir %s", p.Root)
	}

	if err = os.MkdirAll(p.Root, 0777); err != nil {
		return errors.Wrapf(err, "cannot create dir %s", p.Root)
	}

	goModFileName := filepath.Join(p.Root, "go.mod")
	if err = goModInit(goModFileName, p.GitRepo); err != nil {
		return errors.Wrapf(err, "cannot create go.mod file %s", goModFileName)
	}
	log.Printf("Created `go.mod` under %s", goModFileName)

	return nil
}

const GO_MOD_TEMPLATE = `
module {{ .Repository }}

go 1.17

replace github.com/go-openapi/strfmt => github.com/kubewarden/strfmt v0.1.0
`

func goModInit(fileName, gitRepo string) error {
	templateData := struct {
		Repository string
	}{
		Repository: gitRepo,
	}

	goModTemplate, err := template.New("go.mod").Parse(GO_MOD_TEMPLATE)
	if err != nil {
		return err
	}

	file, err := os.Create(fileName)
	if err != nil {
		return err
	}

	if err := goModTemplate.Execute(file, templateData); err != nil {
		return err
	}

	return file.Close()
}

const EASYJSON_BOOTSTRAP_FILE_CONTENTS = `
package bootstrap

type Bootle struct {
	Message string
}
`

func (p *Project) PrepareEasyjsonEnv() error {
	log.Println("Bootstrapping easyjson")
	bootstrapDir := filepath.Join(p.Root, "bootstrap")
	if err := os.Mkdir(bootstrapDir, 0777); err != nil {
		return fmt.Errorf("Cannot create easyjson bootstrap dir: %v", err)
	}

	bootstrapFile := filepath.Join(bootstrapDir, "bottle.go")
	if err := ioutil.WriteFile(bootstrapFile, []byte(EASYJSON_BOOTSTRAP_FILE_CONTENTS), 0644); err != nil {
		return fmt.Errorf("Cannot create easyjson bootstrap file: %v", err)
	}

	easyjsonDeps := []string{
		"github.com/mailru/easyjson/gen",
		"github.com/mailru/easyjson/jlexer",
		"github.com/mailru/easyjson/jwriter",
	}
	for _, dep := range easyjsonDeps {
		if err := p.RunGoGet(dep); err != nil {
			return err
		}
	}

	if err := p.RunEasyJson([]string{bootstrapFile}); err != nil {
		return errors.Wrapf(err, "error running easyjson against bootstrap file")
	}

	if err := p.RunGoModTidy(); err != nil {
		return errors.Wrapf(err, "error running `go mod tidy`")
	}

	if err := os.RemoveAll(bootstrapDir); err != nil {
		return fmt.Errorf("Cannot remove bootstrap dir: %v", err)
	}

	return nil
}

func (p *Project) RunGoModTidy() error {
	args := []string{"mod", "tidy"}

	return p.runGo(args)
}

func (p *Project) runGo(args []string) error {
	cmdName := "go"

	extraEnv := make(map[string]string)

	// override GOPATH
	extraEnv["GOPATH"] = p.OutputDir
	// Add PATH, needed to find the `go` binary
	extraEnv["PATH"] = os.Getenv("PATH")
	// Add HOME, needed to find the go cache directory
	extraEnv["HOME"] = os.Getenv("HOME")

	return runCmd(cmdName, args, extraEnv, p.Root)
}

func (p *Project) RunGoGet(module string) error {
	args := []string{"get", module}

	return p.runGo(args)
}

func (p *Project) InvokeSwaggerModelGenerator(packageName string) error {
	cmdName := "swagger"

	packageNameChunks := strings.Split(packageName, "/")
	if len(packageNameChunks) < 2 {
		return fmt.Errorf("package name %s doesn't have enough chunks", packageName)
	}

	targetDir := filepath.Join(
		p.Root,
		strings.Join(packageNameChunks[0:len(packageNameChunks)-1], "/"))
	moduleName := packageNameChunks[len(packageNameChunks)-1]
	swaggerFileName := filepath.Join(targetDir, moduleName, "swagger.json")

	args := []string{
		"generate",
		"model",
		"--template-dir",
		p.SwaggerTemplatesDir,
		"--allow-template-override",
		"-f",
		swaggerFileName,
		"-t",
		targetDir,
		"-m",
		moduleName,
	}

	extraEnv := make(map[string]string)
	extraEnv["GOPATH"] = p.OutputDir

	return runCmd(cmdName, args, extraEnv, "")
}

func (p *Project) RunEasyJson(targets []string) error {
	cmdName := "easyjson"
	args := []string{"-all"}
	args = append(args, targets...)

	extraEnv := make(map[string]string)

	// override GOPATH
	extraEnv["GOPATH"] = p.OutputDir
	// Add PATH, needed to find the `go` binary
	extraEnv["PATH"] = os.Getenv("PATH")
	// Add HOME, needed to find the go cache directory
	extraEnv["HOME"] = os.Getenv("HOME")

	return runCmd(cmdName, args, extraEnv, p.OutputDir)
}

func runCmd(cmdName string, args []string, extraEnv map[string]string, dir string) error {
	cmd := exec.Command(cmdName, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, value))
	}

	if dir != "" {
		cmd.Dir = dir
	}

	err := cmd.Run()
	if err != nil {
		log.Printf("CMD: %+v", cmd)
		log.Printf("STDOUT: %s", stdout.String())
		log.Printf("STDERR: %s", stderr.String())
	}
	return err
}
