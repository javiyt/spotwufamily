package main

import (
	"bufio"
	"context"
	"github.com/javiyt/spotwufamily/internal/domain"
	"log"
	"os"

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

	s := domain.NewSearchArtists(spotify.Authenticator{}.NewClient(token))

	l, err := readFile()
	if err != nil {
		panic(err)
	}

	_, _ = s.GetArtists(l)
}

func readFile() ([]string, error) {
	f, err := os.Open("data/groups.txt")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	l := make([]string, 0)
	for scanner.Scan() {
		l = append(l, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return l, nil
}
