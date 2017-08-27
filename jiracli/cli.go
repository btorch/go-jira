package jiracli

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"reflect"
	"strings"

	"github.com/AlecAivazis/survey"
	"github.com/coryb/figtree"
	"github.com/jinzhu/copier"
	shellquote "github.com/kballard/go-shellquote"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
	yaml "gopkg.in/coryb/yaml.v2"
	logging "gopkg.in/op/go-logging.v1"
)

var log = logging.MustGetLogger("jira")

// type JiraCli struct {
// 	jira.Jira `yaml:",inline"`
// 	ConfigDir string
// 	oreoAgent *oreo.Client
// }

type Exit struct {
	Code int
}

type GlobalOptions struct {
	Browse         figtree.BoolOption   `json:"browse,omitempty" yaml:"browse,omitempty"`
	Editor         figtree.StringOption `json:"editor,omitempty" yaml:"editor,omitempty"`
	Endpoint       figtree.StringOption `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	SkipEditing    figtree.BoolOption   `json:"noedit,omitempty" yaml:"noedit,omitempty"`
	PasswordSource figtree.StringOption `json:"password-source,omitempty" yaml:"password-source,omitempty"`
	Template       figtree.StringOption `json:"template,omitempty" yaml:"template,omitempty"`
	User           figtree.StringOption `json:"user,omitempty" yaml:"user,omitempty"`
}

type CommandRegistryEntry struct {
	Help        string
	ExecuteFunc func() error
	UsageFunc   func(*kingpin.CmdClause) error
}

type CommandRegistry struct {
	Command string
	Aliases []string
	Entry   *CommandRegistryEntry
	Default bool
}

// either kingpin.Application or kingpin.CmdClause fit this interface
type kingpinAppOrCommand interface {
	Command(string, string) *kingpin.CmdClause
	GetCommand(string) *kingpin.CmdClause
}

// func New(configDir string) *JiraCli {
// 	agent := oreo.New().WithCookieFile(filepath.Join(homedir(), configDir, "cookies.js"))
// 	return &JiraCli{
// 		ConfigDir: configDir,
// 		Jira: jira.Jira{
// 			UA: agent,
// 		},
// 		oreoAgent: agent,
// 	}
// }

func Register(app *kingpin.Application, reg []CommandRegistry) {
	for _, command := range reg {
		copy := command
		commandFields := strings.Fields(copy.Command)
		var appOrCmd kingpinAppOrCommand = app
		if len(commandFields) > 1 {
			for _, name := range commandFields[0 : len(commandFields)-1] {
				tmp := appOrCmd.GetCommand(name)
				if tmp == nil {
					tmp = appOrCmd.Command(name, "")
				}
				appOrCmd = tmp
			}
		}

		cmd := appOrCmd.Command(commandFields[len(commandFields)-1], copy.Entry.Help)
		for _, alias := range copy.Aliases {
			cmd = cmd.Alias(alias)
		}
		if copy.Default {
			cmd = cmd.Default()
		}
		if copy.Entry.UsageFunc != nil {
			copy.Entry.UsageFunc(cmd)
		}

		cmd.Action(
			func(_ *kingpin.ParseContext) error {
				return copy.Entry.ExecuteFunc()
			},
		)
	}
}

func GlobalUsage(cmd *kingpin.CmdClause, opts *GlobalOptions) error {
	cmd.PreAction(func(_ *kingpin.ParseContext) error {
		if opts.User.Value == "" {
			opts.User = figtree.NewStringOption(os.Getenv("USER"))
		}
		return nil
	})
	cmd.Flag("endpoint", "Base URI to use for Jira").Short('e').SetValue(&opts.Endpoint)
	cmd.Flag("user", "Login name used for authentication with Jira service").Short('u').SetValue(&opts.User)
	return nil
}

func LoadConfigs(cmd *kingpin.CmdClause, fig *figtree.FigTree, opts interface{}) {
	cmd.PreAction(func(_ *kingpin.ParseContext) error {
		os.Setenv("JIRA_OPERATION", cmd.FullCommand())
		// load command specific configs first
		if err := fig.LoadAllConfigs(strings.Join(strings.Fields(cmd.FullCommand()), "_")+".yml", opts); err != nil {
			return err
		}
		// then load generic configs if not already populated above
		return fig.LoadAllConfigs("config.yml", opts)
	})
}

func BrowseUsage(cmd *kingpin.CmdClause, opts *GlobalOptions) {
	cmd.Flag("browse", "Open issue(s) in browser after operation").Short('b').SetValue(&opts.Browse)
}

func EditorUsage(cmd *kingpin.CmdClause, opts *GlobalOptions) {
	cmd.Flag("editor", "Editor to use").SetValue(&opts.Editor)
}

func TemplateUsage(cmd *kingpin.CmdClause, opts *GlobalOptions) {
	cmd.Flag("template", "Template to use for output").Short('t').SetValue(&opts.Template)
}

func (o *GlobalOptions) editFile(fileName string) (changes bool, err error) {
	var editor string
	for _, ed := range []string{o.Editor.Value, os.Getenv("JIRA_EDITOR"), os.Getenv("EDITOR"), "vim"} {
		if ed != "" {
			editor = ed
			break
		}
	}

	if o.SkipEditing.Value {
		return false, nil
	}

	tmpFileNameOrig := fmt.Sprintf("%s.orig", fileName)
	if err := copyFile(fileName, tmpFileNameOrig); err != nil {
		return false, err
	}

	defer func() {
		os.Remove(tmpFileNameOrig)
	}()

	shell, _ := shellquote.Split(editor)
	shell = append(shell, fileName)
	log.Debugf("Running: %#v", shell)
	cmd := exec.Command(shell[0], shell[1:]...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		return false, err
	}

	// now we just need to diff the files to see if there are any changes
	var oldHandle, newHandle *os.File
	var oldStat, newStat os.FileInfo
	if oldHandle, err = os.Open(tmpFileNameOrig); err == nil {
		if newHandle, err = os.Open(fileName); err == nil {
			if oldStat, err = oldHandle.Stat(); err == nil {
				if newStat, err = newHandle.Stat(); err == nil {
					// different sizes, so must have changes
					if oldStat.Size() != newStat.Size() {
						return true, err
					}
					oldBuf, newBuf := make([]byte, 1024), make([]byte, 1024)
					var oldCount, newCount int
					// loop though 1024 bytes at a time comparing the buffers for changes
					for err != io.EOF {
						oldCount, _ = oldHandle.Read(oldBuf)
						newCount, err = newHandle.Read(newBuf)
						if oldCount != newCount {
							return true, nil
						}
						if bytes.Compare(oldBuf[:oldCount], newBuf[:newCount]) != 0 {
							return true, nil
						}
					}
					return false, nil
				}
			}
		}
	}
	return false, err
}

func editLoop(opts *GlobalOptions, input interface{}, output interface{}, submit func() error) error {
	tmpFile, err := tmpTemplate(opts.Template.Value, input)
	if err != nil {
		return err
	}

	confirm := func(msg string) (answer bool) {
		survey.AskOne(
			&survey.Confirm{Message: msg, Default: true},
			&answer,
			nil,
		)
		return
	}

	// we need to copy the original output so that we can restore
	// it on retries in case we try to populate bogus fields that
	// are rejected by the jira service.
	dup := reflect.New(reflect.ValueOf(output).Elem().Type())
	err = copier.Copy(dup.Interface(), output)
	if err != nil {
		return err
	}

	for {
		if !opts.SkipEditing.Value {
			changes, err := opts.editFile(tmpFile)
			if err != nil {
				log.Error(err.Error())
				if confirm("Editor reported an error, edit again?") {
					continue
				}
				panic(Exit{Code: 1})
			}
			if !changes {
				if !confirm("No changes detected, submit anyway?") {
					panic(Exit{Code: 1})
				}
			}
		}
		// parse template
		data, err := ioutil.ReadFile(tmpFile)
		if err != nil {
			return err
		}

		defer func(mapType, iface reflect.Type) {
			yaml.DefaultMapType = mapType
			yaml.IfaceType = iface
		}(yaml.DefaultMapType, yaml.IfaceType)
		yaml.DefaultMapType = reflect.TypeOf(map[string]interface{}{})
		yaml.IfaceType = yaml.DefaultMapType.Elem()

		// restore output incase of retry loop
		err = copier.Copy(output, dup.Interface())
		if err != nil {
			return err
		}

		// HACK HACK HACK we want to trim out all the yaml garbage that is not
		// poplulated, like empty arrays, string values with only a newline,
		// etc.  We need to do this because jira will reject json documents
		// with empty arrays, or empty strings typically.  So here we process
		// the data to a raw interface{} then we fixup the yaml parsed
		// interface, then we serialize to a new yaml document ... then is
		// parsed as the original document to populate the output struct.  Phew.
		var raw interface{}
		if err := yaml.Unmarshal(data, &raw); err != nil {
			log.Error(err.Error())
			if confirm("Invalid YAML syntax, edit again?") {
				continue
			}
			panic(Exit{Code: 1})
		}
		yamlFixup(&raw)
		fixedYAML, err := yaml.Marshal(&raw)
		if err != nil {
			log.Error(err.Error())
			if confirm("Invalid YAML syntax, edit again?") {
				continue
			}
			panic(Exit{Code: 1})
		}

		if err := yaml.Unmarshal(fixedYAML, output); err != nil {
			log.Error(err.Error())
			if confirm("Invalid YAML syntax, edit again?") {
				continue
			}
			panic(Exit{Code: 1})
		}
		// submit template
		if err := submit(); err != nil {
			log.Error(err.Error())
			if confirm("Jira reported an error, edit again?") {
				continue
			}
			panic(Exit{Code: 1})
		}
		break
	}
	return nil
}
