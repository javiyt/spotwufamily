// Package main implements the CLI for the spotwufamily tool.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"

	"github.com/javiyt/spotwufamily/internal/infrastructure"

	"github.com/javiyt/spotwufamily/internal/domain"

	"github.com/zmb3/spotify"
	"golang.org/x/oauth2/clientcredentials"
)

func main() {
	token, err := (&clientcredentials.Config{
		ClientID:     os.Getenv("SPOTIFY_ID"),
		ClientSecret: os.Getenv("SPOTIFY_SECRET"),
		TokenURL:     spotify.TokenURL,
	}).Token(context.Background())
	if err != nil {
		log.Fatalf("couldn't get token: %v", err)
	}

	searchSvc := domain.NewSearchArtists(infrastructure.NewArtistHTTPRepository(spotify.Authenticator{}.NewClient(token)))

	lines, err := readFile()
	if err != nil {
		panic(err)
	}

	_, _ = searchSvc.GetArtists(lines)
}

func readFile() ([]string, error) {
	file, err := os.Open("data/groups.txt")
	if err != nil {
		return nil, fmt.Errorf("error %w reading file", err)
	}

	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)

	lines := make([]string, 0)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error %w reading file", err)
	}

	return lines, nil
}
