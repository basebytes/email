package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"path"
	"strconv"
	"strings"
)

func NewClient(config *Config) (client *Client, err error) {
	client = &Client{}
	if client.user, err = mail.ParseAddress(config.User); err != nil {
		err = fmt.Errorf("Invalid user format %s:%s ", config.User, err.Error())
	} else if config.Server == "" {
		err = fmt.Errorf("Server is empty ")
	} else if strs := strings.Split(config.Server, ":"); len(strs) < 2 {
		err = fmt.Errorf("Invalid Server format %s ", config.Server)
	} else if client.port, err = strconv.Atoi(strs[1]); err != nil {
		err = fmt.Errorf("Invalid port %s ", strs[1])
	} else {
		client.server = strs[0]
		client.auth = smtp.PlainAuth("", client.user.Address, config.Password, client.server)
		if config.MaxQueueSize <= 0 {
			config.MaxQueueSize = defaultMaxQueueSize
		}
		client.queue = make(chan *letter, config.MaxQueueSize)
		client.stopped = make(chan struct{}, 1)
		ctx, cancel := context.WithCancel(context.Background())
		client.cancel = cancel
		go client.waitForLetter(ctx)
	}
	return
}

// SenderName 发送人昵称
func SenderName(name string) Option {
	return func(params *params) {
		params.user = &mail.Address{Name: name, Address: params.user.Address}
		params.Add("From", params.user.String())
	}
}

// ReturnPath 退信地址
func ReturnPath(returnPath string) Option {
	return func(params *params) {
		if returnPath == "" {
			return
		}
		if arr, err := mail.ParseAddress(returnPath); err == nil {
			params.Add("Return-Path", arr.Address)
		}
	}
}

// ReplyTo 回信地址
func ReplyTo(replyTo string) Option {
	return func(params *params) {
		if replyTo == "" {
			return
		}
		if arr, err := mail.ParseAddress(replyTo); err == nil {
			params.Add("Reply-To", arr.Address)
		}
	}
}

// Cc 抄送人列表
func Cc(cc string) Option {
	return func(params *params) {
		if ccAddr, err := mail.ParseAddressList(cc); err == nil {
			params.Add("Cc", addressSlice(ccAddr).String())
			params.addresses = append(params.addresses, addressSlice(ccAddr).Addr()...)
		}
	}
}

// Bcc 密送人列表
func Bcc(bcc string) Option {
	return func(params *params) {
		if bccAddr, err := mail.ParseAddressList(bcc); err == nil {
			params.addresses = append(params.addresses, addressSlice(bccAddr).Addr()...)
		}
	}
}

// Subject 邮件主题
func Subject(subject string) Option {
	return func(params *params) {
		if subject == "" {
			return
		}
		params.Add("Subject", subject)
	}
}

func withBoundary(boundary string) Option {
	return func(params *params) {
		params.headers["Content-Type"] = fmt.Sprintf("%s;boundary=%s", contentTypeMixed, boundary)
	}
}

// HtmlMessage html消息
func HtmlMessage(format string, args ...interface{}) *Message {
	return newMessage(
		contentTypeHtml,
		encodingQuotedPrintable,
		map[string][]string{
			"Content-Disposition": {"inline"},
		},
		fmt.Sprintf(format, args...),
	)
}

// TextMessage 文本消息
func TextMessage(format string, args ...interface{}) *Message {
	return newMessage(
		contentTypeText,
		encodingBase64,
		map[string][]string{
			"Content-Disposition": {"inline"},
		},
		base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(format, args...))),
	)
}

//AttachmentMessage 附件消息
func AttachmentMessage(name, filePath, contentId string) *Message {
	header := map[string][]string{
		"Content-Disposition": {"attachment;filename=" + name},
	}
	if contentId != "" {
		header["Content-ID"] = []string{contentId}
	}

	contentType := contentType8bit
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		contentType = t
	}
	content := ""
	if attData, err := ioutil.ReadFile(filePath); err == nil {
		content = base64.StdEncoding.EncodeToString(attData)
	}
	return newMessage(
		contentType,
		encodingBase64,
		header,
		content,
	)
}

type Client struct {
	server    string
	user      *mail.Address
	auth      smtp.Auth
	port      int
	queue     chan *letter
	isRunning bool
	cancel    context.CancelFunc
	stopped   chan struct{}
}

func (c *Client) Close() {
	c.cancel()
	<-c.stopped
}

func (c *Client) waitForLetter(ctx context.Context) {
	c.isRunning = true
	defer func() {
		c.isRunning = false
		for {
			select {
			case _letter := <-c.queue:
				if err := c.send(_letter); err != nil {
					log.Println(err) //TODO add callback
				}
			default:
				c.stopped <- struct{}{}
				return
			}
		}
	}()
	for {
		select {
		case _letter := <-c.queue:
			if err := c.send(_letter); err != nil {
				log.Println(err) //TODO add callback
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) send(_letter *letter) error {
	client, err := c.smtpClient()
	if err != nil {
		return err
	}
	for _, addr := range _letter.addresses {
		if err = client.Rcpt(addr); err != nil {
			return err
		}
	}
	var writer io.WriteCloser
	if writer, err = client.Data(); err == nil {
		if _, err = writer.Write(_letter.content); err == nil {
			err = writer.Close()
		}
	}
	if err == nil {
		_ = client.Quit()
	}
	return err
}

func (c *Client) smtpClient() (client *smtp.Client, err error) {
	server := fmt.Sprintf("%s:%d", c.server, c.port)
	var conn *tls.Conn
	conn, err = tls.Dial("tcp", server, &tls.Config{ServerName: c.server})
	if err != nil {
		err = fmt.Errorf("连接服务[%s]失败，%s", server, err)
	} else if client, err = smtp.NewClient(conn, c.server); err != nil {
		err = fmt.Errorf("创建客户端失败，%s", err)
	} else if err = client.Hello("localhost"); err != nil {
		err = fmt.Errorf("通信失败，%s", err)
	} else if ok, _ := client.Extension("AUTH"); !ok {
		err = fmt.Errorf("服务器不支持AUTH扩展")
	} else if err = client.Auth(c.auth); err != nil {
		err = fmt.Errorf("身份认证失败，%s", err)
	} else if err = client.Mail(c.user.Address); err != nil {
		err = fmt.Errorf("启动邮件事务失败，%s", err)
	}
	return
}

func (c *Client) Send(to string, messages []*Message, options ...Option) error {
	if !c.isRunning {
		return connectionClosedErr
	}
	toAddr, err := mail.ParseAddressList(to)
	if err != nil {
		return err
	}
	ps := newParams(*c.user, toAddr)
	buffer := bytes.NewBuffer(nil)
	w := multipart.NewWriter(buffer)
	options = append(options, withBoundary(w.Boundary()))
	for _, option := range options {
		option(ps)
	}
	ps.writeHeaders(buffer)
	for _, message := range messages {
		iw, e := w.CreatePart(message.h)
		if e == nil {
			if _, e = iw.Write([]byte(message.c)); e == nil {
				_, e = iw.Write(crlf)
			}
		}
		if e != nil {
			return fmt.Errorf("Write email message error %s ", e.Error())
		}
	}
	_ = w.Close()
	c.queue <- &letter{addresses: ps.addresses, content: buffer.Bytes()}
	return nil
}

func newMessage(contentType, encoding string, headers map[string][]string, content string) *Message {
	m := &Message{h: textproto.MIMEHeader{}, c: content}
	m.h.Set("Content-Type", contentType)
	m.h.Set("Content-Transfer-Encoding", encoding)
	if headers != nil {
		for k, vs := range headers {
			if len(k) == 0 || len(vs) == 0 {
				continue
			}
			for _, v := range vs {
				if len(v) == 0 {
					continue
				}
				m.h.Add(k, v)
			}
		}
	}
	return m
}

var connectionClosedErr = errors.New("Connection closed ")
