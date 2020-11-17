package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/google/go-github/github"
	"github.com/kalexmills/github-vet/cmd/vet-bot/loopclosure"
	"golang.org/x/oauth2"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
)

const FindingsOwner = "kalexmills"
const FindingsRepo = "rangeloop-test-repo"

func main() {
	// TODO: uniformly sample from some source of repositories and vet them one at a time.
	ghToken, ok := os.LookupEnv("GITHUB_TOKEN")

	if !ok {
		log.Fatalln("could not find GITHUB_TOKEN environment variable")
	}
	vetBot := NewVetBot(ghToken)

	VetRepositoryBulk(vetBot, Repository{"kalexmills", "bad-go"})
}

type Repository struct {
	Owner string
	Repo  string
}

type VetBot struct {
	ctx    context.Context
	client *github.Client
}

func NewVetBot(token string) *VetBot {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: string(token)},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)
	return &VetBot{
		ctx:    ctx,
		client: client,
	}
}

type VetResult struct {
	Repository
	RootCommitID string
	Start        token.Position
	End          token.Position
}

func VetRepositoryBulk(bot *VetBot, repo Repository) {
	rootCommitID, err := GetRootCommitID(bot, repo)
	if err != nil {
		log.Printf("failed to retrieve root commit ID for repo %s/%s", repo.Owner, repo.Repo)
		return
	}
	url, _, err := bot.client.Repositories.GetArchiveLink(bot.ctx, repo.Owner, repo.Repo, github.Tarball, nil)
	if err != nil {
		log.Printf("failed to get tar link for %s/%s: %v", repo.Owner, repo.Repo, err)
		return
	}
	fmt.Println(url.String())
	resp, err := http.Get(url.String())
	if err != nil {
		log.Printf("failed to download tar contents: %v", err)
		return
	}
	defer resp.Body.Close()
	unzipped, err := gzip.NewReader(resp.Body)
	if err != nil {
		log.Printf("unable to initialize unzip stream: %v", err)
		return
	}
	reader := tar.NewReader(unzipped)
	fset := token.NewFileSet()
	reporter := ReportFinding(bot, fset, rootCommitID, repo)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("failed to read tar entry")
			break
		}
		name := header.Name
		split := strings.SplitN(name, "/", 2)
		if len(split) < 2 {
			continue // we don't care about this file
		}
		realName := split[1]
		switch header.Typeflag {
		case tar.TypeReg:
			log.Printf("found file %s", realName)
			bytes, err := ioutil.ReadAll(reader)
			if err != nil {
				log.Printf("error reading contents of %s: %v", realName, err)
			}
			VetFile(bytes, realName, fset, reporter)
		}
	}
}

func VetFile(contents []byte, path string, fset *token.FileSet, onFind func(analysis.Diagnostic)) {
	file, err := parser.ParseFile(fset, path, string(contents), parser.AllErrors)
	if err != nil {
		log.Printf("failed to parse file %s: %v", path, err)
	}
	pass := analysis.Pass{
		Fset:     fset,
		Files:    []*ast.File{file},
		Report:   onFind,
		ResultOf: make(map[*analysis.Analyzer]interface{}),
	}
	inspection, err := inspect.Analyzer.Run(&pass)
	if err != nil {
		log.Printf("failed inspection: %v", err)
	}
	pass.ResultOf[inspect.Analyzer] = inspection
	_, err = loopclosure.Analyzer.Run(&pass)
	if err != nil {
		log.Printf("failed analysis: %v", err)
	}
}

func GetRootCommitID(bot *VetBot, repo Repository) (string, error) {
	r, _, err := bot.client.Repositories.Get(bot.ctx, repo.Owner, repo.Repo)
	if err != nil {
		log.Printf("failed to get repo: %v", err)
		return "", err
	}
	defaultBranch := r.GetDefaultBranch()

	// retrieve the root commit of the default branch for the repository
	branch, _, err := bot.client.Repositories.GetBranch(bot.ctx, repo.Owner, repo.Repo, defaultBranch)
	if err != nil {
		log.Printf("failed to get default branch: %v", err)
		return "", err
	}
	return branch.GetCommit().GetSHA(), nil
}

func ReportFinding(bot *VetBot, fset *token.FileSet, rootCommitID string, repo Repository) func(analysis.Diagnostic) {
	return func(d analysis.Diagnostic) {
		HandleVetResult(bot, VetResult{
			Repository:   repo,
			RootCommitID: rootCommitID,
			Start:        fset.Position(d.Pos),
			End:          fset.Position(d.End),
		})
	}
}

func HandleVetResult(bot *VetBot, result VetResult) {
	// TODO: record this finding as structured data somewhere.
	iss, _, err := bot.client.Issues.Create(bot.ctx, FindingsOwner, FindingsRepo, CreateIssueRequest(result))
	if err != nil {
		log.Printf("error opening new issue: %v", err)
		return
	}
	log.Printf("opened new issue at %s", iss.GetURL())
}

func CreateIssueRequest(result VetResult) *github.IssueRequest {
	title := fmt.Sprintf("%s/%s: ", result.Owner, result.Repo)
	permalink := fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s#L%d-L%d", result.Owner, result.Repo, result.RootCommitID, result.Start.Filename, result.Start.Line, result.End.Line)

	// TODO: make the issue prettier; include a snippet of the source and link to it
	// Also provide some context as to why the bot thinks the code is wrong.
	body := "Found an issue at " + permalink
	return &github.IssueRequest{
		Title: &title,
		Body:  &body,
	}
}
