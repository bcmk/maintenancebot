package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	tg "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Version is the version of the current code state
var Version = "(devel)"

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}

func lerr(format string, v ...interface{}) {
	log.Printf("[ERROR] "+format, v...)
}

func linf(format string, v ...interface{}) {
	log.Printf("[INFO] "+format, v...)
}

func ldbg(format string, v ...interface{}) {
	log.Printf("[DEBUG] "+format, v...)
}

type incomingPacket struct {
	message  tg.Update
	endpoint string
}

type worker struct {
	bots     map[string]*tg.BotAPI
	cfg      *config
	botNames map[string]string
	ourIDs   []int64
}

// NoRedirect tells HTTP client not to redirect
func NoRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

// HTTPClientWithTimeoutAndAddress returns HTTP client bound to specific IP address
func HTTPClientWithTimeoutAndAddress(timeoutSeconds int, address string, cookies bool) *http.Client {
	return &http.Client{
		CheckRedirect: NoRedirect,
		Timeout:       time.Second * time.Duration(timeoutSeconds),
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   time.Second * time.Duration(timeoutSeconds),
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       http.DefaultTransport.(*http.Transport).IdleConnTimeout,
			TLSHandshakeTimeout:   time.Second * time.Duration(timeoutSeconds),
			ExpectContinueTimeout: time.Duration(0),
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
}

func newWorker(args []string) *worker {
	if len(args) != 1 {
		panic("usage: maintanencebot <config>")
	}
	cfg := readConfig(args[0])

	var err error

	telegramClient := HTTPClientWithTimeoutAndAddress(cfg.TelegramTimeoutSeconds, "", false)
	bots := make(map[string]*tg.BotAPI)
	for n, p := range cfg.Endpoints {
		//noinspection GoNilness
		var bot *tg.BotAPI
		bot, err = tg.NewBotAPIWithClient(p.BotToken, tg.APIEndpoint, telegramClient)
		checkErr(err)
		bots[n] = bot
	}
	w := &worker{
		bots:     bots,
		cfg:      cfg,
		botNames: map[string]string{},
		ourIDs:   cfg.getOurIDs(),
	}

	return w
}

func (w *worker) setWebhook() {
	for n, p := range w.cfg.Endpoints {
		linf("setting webhook for endpoint %s for domain %s...", n, p.WebhookDomain)
		wh, err := tg.NewWebhook(path.Join(p.WebhookDomain, p.ListenPath))
		checkErr(err)
		_, err = w.bots[n].Request(wh)
		checkErr(err)
		info, err := w.bots[n].GetWebhookInfo()
		checkErr(err)
		if info.LastErrorDate != 0 {
			linf("last webhook error time: %v", time.Unix(int64(info.LastErrorDate), 0))
		}
		if info.LastErrorMessage != "" {
			linf("last webhook error message: %s", info.LastErrorMessage)
		}
		linf("OK")
	}
}

func (w *worker) removeWebhook() {
	for n := range w.cfg.Endpoints {
		linf("removing webhook for endpoint %s...", n)
		_, err := w.bots[n].Request(tg.DeleteWebhookConfig{})
		checkErr(err)
		linf("OK")
	}
}

func (w *worker) initBotNames() {
	for n := range w.cfg.Endpoints {
		user, err := w.bots[n].GetMe()
		checkErr(err)
		linf("bot name for endpoint %s: %s", n, user.UserName)
		w.botNames[n] = user.UserName
	}
}

func (w *worker) serveEndpoints() {
	go func() {
		err := http.ListenAndServe(w.cfg.ListenAddress, nil)
		checkErr(err)
	}()
}

func (w *worker) logConfig() {
	cfgString, err := json.MarshalIndent(w.cfg, "", "    ")
	checkErr(err)
	linf("config: " + string(cfgString))
}

func (w *worker) sendMessage(endpoint string, chatID int64, msg string) {
	if _, err := w.bots[endpoint].Send(tg.NewMessage(chatID, msg)); err != nil {
		lerr("cannot send a message, error: %v", err)
	}
}

func (w *worker) processIncomingCommand(endpoint string, chatID int64, command, arguments string, now int) {
	command = strings.ToLower(command)
	linf("chat: %d, command: %s %s", chatID, command, arguments)
	w.sendMessage(endpoint, chatID, w.cfg.Endpoints[endpoint].MaintenanceResponse)
}

func getCommandAndArgs(update tg.Update, mention string, ourIDs []int64) (int64, string, string) {
	var text string
	var chatID int64
	var forceMention bool
	if update.Message != nil && update.Message.Chat != nil {
		text = update.Message.Text
		chatID = update.Message.Chat.ID
		if update.Message.NewChatMembers != nil {
			ldbg("we got new members")
			for _, m := range update.Message.NewChatMembers {
				for _, ourID := range ourIDs {
					if int64(m.ID) == ourID {
						ldbg("we were added to group")
						return chatID, "start", ""
					}
				}
			}
		}
	} else if update.ChannelPost != nil && update.ChannelPost.Chat != nil {
		ldbg("we got channel post")
		text = update.ChannelPost.Text
		chatID = update.ChannelPost.Chat.ID
		forceMention = true
	} else if update.CallbackQuery != nil && update.CallbackQuery.From != nil {
		ldbg("we got callback query")
		text = update.CallbackQuery.Data
		chatID = int64(update.CallbackQuery.From.ID)
	}
	text = strings.TrimLeft(text, " /")
	if text == "" {
		ldbg("we got nothing")
		return 0, "", ""
	}
	parts := strings.SplitN(text, " ", 2)
	if strings.HasSuffix(parts[0], mention) {
		parts[0] = parts[0][:len(parts[0])-len(mention)]
	} else if forceMention {
		ldbg("we were not mentioned")
		return 0, "", ""
	}
	for len(parts) < 2 {
		return chatID, parts[0], ""
	}
	return chatID, parts[0], strings.TrimSpace(parts[1])
}

func (w *worker) processTGUpdate(p incomingPacket) {
	ldbg("got Telegram update")
	now := int(time.Now().Unix())
	u := p.message
	mention := "@" + w.botNames[p.endpoint]
	chatID, command, args := getCommandAndArgs(u, mention, w.ourIDs)
	if u.CallbackQuery != nil {
		callback := tg.CallbackConfig{CallbackQueryID: u.CallbackQuery.ID}
		_, err := w.bots[p.endpoint].Request(callback)
		if err != nil {
			lerr("cannot answer callback query, %v", err)
		}
	}
	if command != "" {
		w.processIncomingCommand(p.endpoint, chatID, command, args, now)
	}
}

func (c *config) getOurIDs() []int64 {
	var ids []int64
	for _, e := range c.Endpoints {
		if idx := strings.Index(e.BotToken, ":"); idx != -1 {
			id, err := strconv.ParseInt(e.BotToken[:idx], 10, 64)
			checkErr(err)
			ids = append(ids, id)
		} else {
			checkErr(errors.New("cannot get our ID"))
		}
	}
	return ids
}

func (w *worker) incoming() chan incomingPacket {
	result := make(chan incomingPacket)
	for name, endpoint := range w.cfg.Endpoints {
		linf("listening for a webhook for endpoint %s for domain %s", name, endpoint.WebhookDomain)
		incoming := w.bots[name].ListenForWebhook(endpoint.WebhookDomain + endpoint.ListenPath)
		go func(n string, incoming tg.UpdatesChannel) {
			for i := range incoming {
				result <- incomingPacket{message: i, endpoint: n}
			}
		}(name, incoming)
	}
	return result
}

func main() {
	version := flag.Bool("v", false, "prints current version")
	flag.Parse()
	if *version {
		fmt.Println(Version)
		os.Exit(0)
	}

	w := newWorker(flag.Args())
	w.logConfig()
	w.setWebhook()
	w.initBotNames()
	w.serveEndpoints()

	signals := make(chan os.Signal, 16)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGABRT)
	_, err := w.bots[w.cfg.AdminEndpoint].Send(tg.NewMessage(w.cfg.AdminID, "bot is up in maintenance mode"))
	checkErr(err)
	incoming := w.incoming()
	for {
		select {
		case u := <-incoming:
			w.processTGUpdate(u)
		case s := <-signals:
			linf("got signal %v", s)
			if s == syscall.SIGINT || s == syscall.SIGTERM || s == syscall.SIGABRT {
				w.removeWebhook()
				return
			}
		}
	}
}
