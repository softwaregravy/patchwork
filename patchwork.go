package patchwork

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/f2prateek/go-circle"
	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

// Patchwork lets you apply a patch across repos.
type Patchwork struct {
	github *github.Client
	circle circle.CircleCI
}

// New creates a Patchwork client.
func New(githubToken, circleToken string) *Patchwork {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: githubToken},
	)
	tc := oauth2.NewClient(oauth2.NoContext, ts)

	return &Patchwork{
		github: github.NewClient(tc),
		circle: circle.New(circleToken),
	}
}

// Repository is a repository to be patched.
type Repository struct {
	Owner string
	Repo  string
}

// ApplyOptions holds arguments provided to an apply operation.
type ApplyOptions struct {
	Message string
	Branch  string
	Repos   []Repository
}

// Apply the given patch across the given repos.
func (patchwork *Patchwork) Apply(opts ApplyOptions, patch func(repo *github.Repository, directory string)) {
	reposC := make(chan Repository)
	done := make(chan bool)

	go func() {
		var wg sync.WaitGroup
		for repo := range reposC {
			wg.Add(1)

			go func(repo Repository) {
				defer wg.Done()

				var summary circle.BuildSummary
				for {
					summaries, err := patchwork.circle.RecentBuildsForProject(repo.Owner, repo.Repo)
					if err != nil {
						log.Fatal("couldn't get recent builds for repo", repo, err)
					}

					summary = latestSummary(opts.Branch, summaries)
					if summary.Lifecycle == "finished" {
						break
					}
					time.Sleep(2 * time.Minute)
				}

				if summary.Outcome == "success" {
					fmt.Println(repo, "succeeeded")
				} else {
					fmt.Println(repo, "failed", summary)
				}

			}(repo)
		}
		wg.Wait()
		done <- true
	}()

	for _, repo := range opts.Repos {
		repository, _, err := patchwork.github.Repositories.Get(repo.Owner, repo.Repo)
		if err != nil {
			log.Fatal("could not fetch github information", err)
		}

		dir, err := ioutil.TempDir("", strconv.Itoa(*repository.ID))
		if err != nil {
			log.Fatal("could not create temporary directory", err)
		}
		defer os.Remove(dir)

		patchwork.run(dir, "git", "clone", *repository.SSHURL, dir)
		// Checking out a branch is probably unnecessary.
		patchwork.run(dir, "git", "checkout", "-b", opts.Branch)

		if err := os.Chdir(dir); err != nil {
			log.Fatal("could not change directory", err)
		}

		patch(repository, dir)

		patchwork.run(dir, "git", "add", "-A")
		patchwork.run(dir, "git", "commit", "-m", opts.Message)
		patchwork.run(dir, "git", "push", "origin", opts.Branch)

		reposC <- repo
	}

	<-done
}

func latestSummary(branch string, summaries []circle.BuildSummary) circle.BuildSummary {
	for _, summary := range summaries {
		if summary.Branch == branch {
			return summary
		}
	}
	return circle.BuildSummary{}
}

func (patchwork *Patchwork) run(dir, name string, args ...string) {
	command := exec.Command(name, args...)
	var out bytes.Buffer
	command.Stdout = &out
	command.Stderr = &out
	command.Dir = dir
	if err := command.Run(); err != nil {
		log.Println("could not run", name, args)
		log.Println(out.String())
		log.Fatal(err)
	}
}
