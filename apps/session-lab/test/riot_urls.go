// Package test holds live integration tests for session-lab. Everything in it is
// test-only: the riot pages below never reach the runtime, which only ever sees a
// list of URLs.
package test

// The champion list and the pages built from it. Kept here, not in the runner.
var riotChampions = []string{
	"Yasuo", "LeBlanc", "Ahri", "Darius", "Jinx", "Lux", "Ezreal", "Katarina",
	"Garen", "Thresh", "LeeSin", "Vayne", "Zed", "Akali", "Riven", "Sona",
	"Teemo", "Ashe",
}

const (
	// riotHeavy is a real single-page app: it renders, it pulls script, it costs
	// CPU. Session 1 loads it so every other session has a noisy neighbour.
	riotHeavy = "https://www.leagueoflegends.com/pt-br/champions/leblanc/"
	// riotSplashPrefix is the CDN path for a champion splash image.
	riotSplashPrefix = "https://ddragon.leagueoflegends.com/cdn/img/champion/splash"
)

// riotURLs builds the page list for n sessions: the heavy page first, then one
// splash per session with the champion rotating and the skin number stepping once
// the champion list wraps. Two sessions never get the same URL while
// n <= len(champions) * maxSkins.
func riotURLs(n int) []string {
	if n <= 0 {
		return nil
	}
	urls := make([]string, 0, n)
	urls = append(urls, riotHeavy)
	for index := 1; index < n; index++ {
		position := index - 1
		champion := riotChampions[position%len(riotChampions)]
		skin := position / len(riotChampions)
		urls = append(urls, riotSplashPrefix+"/"+champion+"_"+itoa(skin)+".jpg")
	}
	return urls
}

// itoa avoids pulling strconv in for one small job.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
