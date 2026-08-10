package siteknowledge

import "testing"

func TestMatchTargetPrefersMostSpecificAlias(t *testing.T) {
	definition, ok := MatchTarget("Open YouTube Music and play a song")
	if !ok || definition.ID != "youtube-music" {
		t.Fatalf("definition = %#v, %v", definition, ok)
	}
}

func TestMatchURLPrefersMostSpecificHost(t *testing.T) {
	definition, ok := MatchURL("https://music.youtube.com/watch?v=one")
	if !ok || definition.ID != "youtube-music" {
		t.Fatalf("definition = %#v, %v", definition, ok)
	}
}

func TestSearchURLIsDeclarative(t *testing.T) {
	definition, _ := MatchTarget("Reddit")
	if got := SearchURL(definition, "golang agents"); got != "https://www.reddit.com/search/?q=golang+agents" {
		t.Fatalf("search URL = %q", got)
	}
}

func TestSearchURLUsesTypedTemplate(t *testing.T) {
	definition, _ := MatchTarget("GitHub")
	if !HasScopedSearch(definition, "repository") {
		t.Fatal("GitHub repository search should be scoped")
	}
	if got := SearchURL(definition, "golang agents", "repository"); got != "https://github.com/search?q=golang+agents&type=repositories" {
		t.Fatalf("typed search URL = %q", got)
	}
}

func TestNetflixUsesDirectBrowseAndSearchRoutes(t *testing.T) {
	definition, ok := MatchTarget("Open Netflix home page")
	if !ok || definition.ID != "netflix" || definition.HomeURL != "https://www.netflix.com/browse" {
		t.Fatalf("Netflix definition = %#v, ok=%v", definition, ok)
	}
	if got := SearchURL(definition, "Stranger Things"); got != "https://www.netflix.com/search?q=Stranger+Things" {
		t.Fatalf("Netflix search URL = %q", got)
	}
}

func TestKnownMediaSitesDeclareTitleContinuation(t *testing.T) {
	for _, target := range []string{"YouTube", "QQ Music", "Bilibili", "YouTube Music", "Netflix", "Spotify"} {
		definition, ok := MatchTarget(target)
		if !ok || definition.MediaContinuation == nil || !definition.MediaContinuation.OpenFirst || !definition.MediaContinuation.Play {
			t.Fatalf("%s media continuation = %#v, ok=%v", target, definition.MediaContinuation, ok)
		}
	}
}
