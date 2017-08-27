package jiracli

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/url"
	"strings"

	"github.com/coryb/figtree"
	"github.com/coryb/oreo"

	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

type RequestOptions struct {
	GlobalOptions `yaml:",inline" figtree:",inline"`
	Method        string
	URI           string
	Data          string
}

func CmdRequestRegistry(fig *figtree.FigTree, o *oreo.Client) *CommandRegistryEntry {
	opts := RequestOptions{
		GlobalOptions: GlobalOptions{
			Template: figtree.NewStringOption("request"),
		},
		Method: "GET",
	}

	return &CommandRegistryEntry{
		"Open issue in requestr",
		func() error {
			return CmdRequest(o, &opts)
		},
		func(cmd *kingpin.CmdClause) error {
			LoadConfigs(cmd, fig, &opts)
			return CmdRequestUsage(cmd, &opts)
		},
	}
}

func CmdRequestUsage(cmd *kingpin.CmdClause, opts *RequestOptions) error {
	if err := GlobalUsage(cmd, &opts.GlobalOptions); err != nil {
		return err
	}
	cmd.Flag("method", "HTTP request method to use").Short('m').EnumVar(&opts.Method, "GET", "PUT", "POST", "DELETE")
	cmd.Arg("API", "Path to Jira API (ie: /rest/api/2/issue)").Required().StringVar(&opts.URI)
	cmd.Arg("JSON", "JSON Content to send to API").Required().StringVar(&opts.Data)

	return nil
}

// CmdRequest open the default system requestr to the provided issue
func CmdRequest(o *oreo.Client, opts *RequestOptions) error {
	uri := opts.URI
	if !strings.HasPrefix(uri, "http") {
		uri = opts.Endpoint.Value + uri
	}

	parsedURI, err := url.Parse(uri)
	if err != nil {
		return err
	}
	builder := oreo.RequestBuilder(parsedURI).WithMethod(opts.Method)
	if opts.Data != "" {
		builder = builder.WithJSON(opts.Data)
	}

	resp, err := o.Do(builder.Build())
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	content, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		fmt.Println("No Content")
		return nil
	}
	var data interface{}
	err = json.Unmarshal(content, &data)
	if err != nil {
		return fmt.Errorf("JSON Parse Error: %s from %q", err, content)
	}

	return runTemplate(opts.Template.Value, &data, nil)
}
