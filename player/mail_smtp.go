package main

// mail_smtp.go — sending a reply or a new message from the Mail app
// (docs/EMAIL_CALENDAR.md).
//
// net/smtp over TLS from the first byte (465) or STARTTLS (587; a server
// that does not offer it is refused before the password goes out). Sign-in is
// AUTH PLAIN, AUTH LOGIN for servers that only offer that (Outlook), or
// XOAUTH2 with a Microsoft access token. Certificates are checked against the
// system roots plus the firmware fallback.
//
// Messages are plain text only: UTF-8, quoted-printable, a Subject in RFC
// 2047 words, a Message-ID, and In-Reply-To/References when it answers
// another message. Every header value is stripped of line breaks before it is
// written, so nothing typed can add a header.
//
// Gmail and Outlook file what was sent in Sent themselves; for every other
// provider the message is also appended to the account's Sent folder.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// mailOutgoing is a message as the compose screen hands it over.
type mailOutgoing struct {
	To         string // comma- or semicolon-separated, as typed
	Cc         string
	Subject    string
	Body       string
	InReplyTo  string // the answered message's Message-ID
	References string
}

// mailMaxRecipients bounds To and Cc together.
const mailMaxRecipients = 50

// mailHeaderSafe strips what could end a header line.
func mailHeaderSafe(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "\x00", "").Replace(s)
	return strings.TrimSpace(s)
}

// mailParseRecipients reads a typed address list.
func mailParseRecipients(s string) ([]*mail.Address, error) {
	s = strings.TrimSpace(strings.ReplaceAll(mailHeaderSafe(s), ";", ","))
	s = strings.Trim(s, ", ")
	if s == "" {
		return nil, nil
	}
	list, err := mail.ParseAddressList(s)
	if err != nil {
		return nil, &mailError{kind: mailErrServer, msg: "an address does not look right: " + mailClean(s, 80)}
	}
	return list, nil
}

// mailMessageID makes a new Message-ID at the sender's domain.
func mailMessageID(email string, now time.Time) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	domain := mailcalDomain(email)
	if domain == "" {
		domain = "jukahub.invalid"
	}
	return "<" + hex.EncodeToString(b[:]) + "." + strconv.FormatInt(now.UnixNano(), 36) + "@" + domain + ">"
}

// mailJoinAddrs writes an address header's value, one address per folded
// line when there are several.
func mailJoinAddrs(list []*mail.Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = a.String()
	}
	return strings.Join(parts, ",\r\n ")
}

// mailMsgIDList keeps only <…> message IDs from a References-like value,
// each once.
func mailMsgIDList(s string, max int) string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.Fields(mailHeaderSafe(s)) {
		if strings.HasPrefix(f, "<") && strings.HasSuffix(f, ">") && len(f) < 250 && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	if len(out) > max {
		// The first (the thread's root) and the newest ones matter.
		out = append(out[:1], out[len(out)-max+1:]...)
	}
	return strings.Join(out, " ")
}

// mailBuildMessage writes the RFC 5322 message. Pure apart from the random
// Message-ID.
func mailBuildMessage(a mailcalAccount, to, cc []*mail.Address, m mailOutgoing, now time.Time) ([]byte, string) {
	var b bytes.Buffer
	from := mail.Address{Name: mailHeaderSafe(a.Name), Address: strings.TrimSpace(a.Email)}
	id := mailMessageID(a.Email, now)
	header := func(k, v string) {
		if v != "" {
			b.WriteString(k + ": " + v + "\r\n")
		}
	}
	header("Date", now.Format(time.RFC1123Z))
	header("From", from.String())
	header("To", mailJoinAddrs(to))
	header("Cc", mailJoinAddrs(cc))
	// A long subject is several encoded words; each goes on its own folded
	// line so no line passes the 998-character limit.
	header("Subject", strings.ReplaceAll(mime.QEncoding.Encode("utf-8", mailHeaderSafe(m.Subject)), "?= =?", "?=\r\n =?"))
	header("Message-ID", id)
	header("In-Reply-To", mailMsgIDList(m.InReplyTo, 1))
	header("References", mailMsgIDList(m.References, 20))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	header("User-Agent", "JukaHub")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	body := strings.ReplaceAll(m.Body, "\r\n", "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	_, _ = qp.Write([]byte(body))
	_ = qp.Close()
	return b.Bytes(), id
}

// ── SMTP auth ───────────────────────────────────────────────────────────

// mailLoginAuth is AUTH LOGIN, for servers that offer nothing else.
type mailLoginAuth struct{ user, pass string }

func (a *mailLoginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("unencrypted connection")
	}
	return "LOGIN", nil, nil
}

func (a *mailLoginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	prompt := strings.ToLower(string(fromServer))
	switch {
	case strings.HasPrefix(prompt, "user"):
		return []byte(a.user), nil
	case strings.HasPrefix(prompt, "pass"):
		return []byte(a.pass), nil
	}
	return nil, errors.New("unexpected server challenge")
}

// mailXOAuth2Auth is XOAUTH2 with an access token.
type mailXOAuth2Auth struct{ user, token string }

func (a *mailXOAuth2Auth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("unencrypted connection")
	}
	return "XOAUTH2", mailXOAuth2(a.user, a.token), nil
}

func (a *mailXOAuth2Auth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		// The server's error details: an empty answer lets it finish with
		// its refusal.
		return []byte{}, nil
	}
	return nil, nil
}

// mailSMTPErr turns an SMTP reply into a mail error.
func mailSMTPErr(err error, auth bool) error {
	var te *textproto.Error
	if errors.As(err, &te) {
		msg := mailClean(te.Msg, 160)
		if auth || te.Code == 535 || te.Code == 534 || te.Code == 530 {
			return &mailError{kind: mailErrAuth, msg: setOr(msg, "the outgoing server refused the sign-in"), detail: msg}
		}
		return &mailError{kind: mailErrServer, msg: "the outgoing server refused: " + setOr(msg, strconv.Itoa(te.Code)), detail: msg}
	}
	return mailNetErr(err)
}

// ── Sending ─────────────────────────────────────────────────────────────

// mailSMTPOpen connects, encrypts and signs in.
func mailSMTPOpen(ctx context.Context, a mailcalAccount) (*smtp.Client, func(), error) {
	host := strings.TrimSpace(a.SMTPHost)
	if host == "" || a.SMTPPort <= 0 || a.SMTPPort > 65535 {
		return nil, nil, &mailError{kind: mailErrServer, msg: "the outgoing mail (SMTP) server is not set"}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(a.SMTPPort))
	d := &net.Dialer{Timeout: 20 * time.Second}
	starttls := a.SMTPSec == mailcalStartTLS
	var conn net.Conn
	var err error
	if starttls {
		conn, err = d.DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&tls.Dialer{NetDialer: d, Config: mailTLSConfig(host)}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, mailNetErr(err)
	}
	_ = conn.SetDeadline(time.Now().Add(90 * time.Second))
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	done := func() {
		stop()
		_ = conn.Close()
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		done()
		return nil, nil, mailSMTPErr(err, false)
	}
	if starttls {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			done()
			return nil, nil, &mailError{kind: mailErrInsecure, msg: "the outgoing server on port " + strconv.Itoa(a.SMTPPort) +
				" does not offer encryption (STARTTLS), so JukaHub will not send your password to it"}
		}
		if err := c.StartTLS(mailTLSConfig(host)); err != nil {
			done()
			return nil, nil, mailSMTPErr(err, false)
		}
	}
	var auth smtp.Auth
	_, mechs := c.Extension("AUTH")
	mechs = strings.ToUpper(mechs)
	if a.OAuth == mailcalOAuthMicrosoft && a.RefreshToken != "" {
		token, err := mailMSAccessToken(ctx, a)
		if err != nil {
			done()
			return nil, nil, err
		}
		auth = &mailXOAuth2Auth{user: a.user(), token: token}
	} else {
		pass := mailcalOpen(a.Password, mailcalKeyPassword)
		if pass == "" {
			done()
			return nil, nil, &mailError{kind: mailErrAuth, msg: "no password is saved for this account"}
		}
		if !strings.Contains(mechs, "PLAIN") && strings.Contains(mechs, "LOGIN") {
			auth = &mailLoginAuth{user: a.user(), pass: pass}
		} else {
			auth = smtp.PlainAuth("", a.user(), pass, host)
		}
	}
	if err := c.Auth(auth); err != nil {
		done()
		return nil, nil, mailSMTPErr(err, true)
	}
	return c, done, nil
}

// mailSentAutomatically reports providers that file sent mail in Sent
// themselves, where an APPEND would make a second copy.
func mailSentAutomatically(a mailcalAccount) bool {
	return a.Provider == "gmail" || a.Provider == "outlook" || a.OAuth == mailcalOAuthMicrosoft
}

// mailSend sends m from account a and, where the provider does not do it,
// files a copy in Sent. Any goroutine; bounded by ctx.
func mailSend(ctx context.Context, a mailcalAccount, m mailOutgoing) error {
	to, err := mailParseRecipients(m.To)
	if err != nil {
		return err
	}
	cc, err := mailParseRecipients(m.Cc)
	if err != nil {
		return err
	}
	if len(to)+len(cc) == 0 {
		return &mailError{kind: mailErrServer, msg: "add who the message is for"}
	}
	if len(to)+len(cc) > mailMaxRecipients {
		return &mailError{kind: mailErrServer, msg: "too many recipients for one message"}
	}
	msg, _ := mailBuildMessage(a, to, cc, m, time.Now())
	c, done, err := mailSMTPOpen(ctx, a)
	if err != nil {
		return err
	}
	defer done()
	if err := c.Mail(strings.TrimSpace(a.Email)); err != nil {
		return mailSMTPErr(err, false)
	}
	for _, r := range append(append([]*mail.Address(nil), to...), cc...) {
		if err := c.Rcpt(r.Address); err != nil {
			e := mailSMTPErr(err, false)
			var me *mailError
			if errors.As(e, &me) && me.kind == mailErrServer {
				me.msg = "the server would not send to " + mailClean(r.Address, 80) + ": " + setOr(me.detail, "refused")
			}
			return e
		}
	}
	w, err := c.Data()
	if err != nil {
		return mailSMTPErr(err, false)
	}
	if _, err := w.Write(msg); err != nil {
		return mailSMTPErr(err, false)
	}
	if err := w.Close(); err != nil {
		return mailSMTPErr(err, false)
	}
	_ = c.Quit()
	if !mailSentAutomatically(a) {
		// The message is sent: filing the copy does not hold up "Sent".
		go func() {
			actx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := mailAppendSent(actx, a, msg); err != nil {
				logInfo("mail", "the sent message was not filed in Sent: %s", firstLine(err.Error()))
			}
		}()
	}
	return nil
}

// mailAppendSent files a sent message in the account's Sent folder.
func mailAppendSent(ctx context.Context, a mailcalAccount, msg []byte) error {
	c, err := mailIMAPOpen(ctx, a)
	if err != nil {
		return err
	}
	defer c.logout()
	folders := mailCachedFolders(a.ID)
	if folders.Sent == "" {
		list, err := c.list()
		if err != nil {
			return err
		}
		folders = imapPickFolders(list)
	}
	if folders.Sent == "" {
		return errors.New("no Sent folder")
	}
	return c.appendMsg(folders.Sent, msg)
}
