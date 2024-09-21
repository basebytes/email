package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/smtp"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
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
		params.Add(headerFrom, params.user.String())
	}
}

// ReturnPath 退信地址
func ReturnPath(returnPath string) Option {
	return func(params *params) {
		if returnPath == "" {
			return
		}
		if arr, err := mail.ParseAddress(returnPath); err == nil {
			params.Add(headerReturnPath, arr.Address)
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
			params.Add(headerReplyTo, arr.Address)
		}
	}
}

// Cc 抄送人列表
func Cc(cc string) Option {
	return func(params *params) {
		if ccAddr, err := mail.ParseAddressList(cc); err == nil {
			params.Add(headerCc, addressSlice(ccAddr).String())
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
		params.Add(headerSubject, subject)
	}
}

// Callback 回调函数
func Callback(f func(string, []string, []byte, error)) Option {
	return func(params *params) {
		if f != nil {
			params.callback = f
		}
	}
}

// Key 邮件标识
func Key(key string) Option {
	return func(params *params) {
		params.key = key
	}
}

// HtmlMessage html消息
func HtmlMessage(format string, args ...interface{}) *Message {
	return newMessage(
		contentTypeHtml,
		encodingQuotedPrintable,
		map[string][]string{
			headerContentDisposition: {"inline"},
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
			headerContentDisposition: {"inline"},
		},
		base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(format, args...))),
	)
}

//AttachmentFileMessage 附件消息
func AttachmentFileMessage(name, contentId, filePath string) *Message {
	header, contentType := getAttachmentHeader(name, contentId)
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

//AttachmentRawDataMessage 附件消息
func AttachmentRawDataMessage(name, contentId string, attRawData []byte) *Message {
	header, contentType := getAttachmentHeader(name, contentId)
	return newMessage(
		contentType,
		encodingBase64,
		header,
		base64.StdEncoding.EncodeToString(attRawData),
	)
}

//ParseContent 解析邮件内容
func ParseContent(content []byte) (emailContent *Content) {
	emailContent = NewContent()
	_boundary := extractBoundary(content, emailContent)
	contentSlice := bytes.Split(content, _boundary)
	for i := 1; i < len(contentSlice); i++ {
		contents := bytes.Split(contentSlice[i], crlf)
		if c, name, htmlContent := parse(contents); name != "" {
			emailContent.AppendAtt(NewAttachment(name))
			emailContent.AppendAttContent(c)
		} else if htmlContent {
			emailContent.Html += string(c)
		} else {
			dest := make([]byte, base64.StdEncoding.DecodedLen(len(c)))
			_, _ = base64.StdEncoding.Decode(dest, c)
			emailContent.Html = string(dest)
		}
	}
	return
}

func parse(contents [][]byte) (content []byte, fileName string, htmlContent bool) {
	for i, c := range contents {
		if len(c) == 0 {
			continue
		}
		if kv := bytes.Split(c, kvSeparator); len(kv) == 2 {
			if k := string(kv[0]); k == headerContentDisposition {
				if f := bytes.Split(kv[1], filename); len(f) == 2 {
					fileName = string(f[1])
				}
			} else if k == headerContentType {
				htmlContent = string(c) == contentTypeHtml
			}
		} else {
			content = bytes.TrimSpace(bytes.Join(contents[i:], crlf))
			break
		}
	}
	return
}

func extractBoundary(content []byte, emailContent *Content) []byte {
	var (
		contentSlice = bytes.Split(content, crlf)
		_boundary    = []byte{'-', '-'}
	)
	for _, c := range contentSlice {
		if len(c) == 0 {
			continue
		}
		if kv := bytes.Split(c, kvSeparator); len(kv) == 2 {
			switch k := string(kv[0]); k {
			case headerSubject:
				emailContent.Subject = string(kv[1])
			case headerContentType:
				if b := bytes.Split(kv[1], boundary); len(b) == 2 {
					_boundary = append(_boundary, b[1]...)
				}
			}
		} else if bytes.Contains(c, _boundary) {
			break
		}
	}
	return _boundary
}

func withBoundary(boundary string) Option {
	return func(params *params) {
		params.headers[headerContentType] = fmt.Sprintf("%s;boundary=%s", contentTypeMixed, boundary)
	}
}

func getAttachmentHeader(name, contentId string) (map[string][]string, string) {
	header := map[string][]string{
		headerContentDisposition: {"attachment;filename=" + name},
	}
	if contentId != "" {
		header[headerContentID] = []string{contentId}
	}
	contentType := contentType8bit
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		contentType = t
	}
	return header, contentType
}

type Client struct {
	server    string
	user      *mail.Address
	auth      smtp.Auth
	port      int
	queue     chan *letter
	cancel    context.CancelFunc
	stopped   chan struct{}
	isRunning int32
}

func (c *Client) Close() {
	c.cancel()
	<-c.stopped
}

func (c *Client) Closed() bool {
	return atomic.LoadInt32(&c.isRunning) == 0
}

func (c *Client) Send(to string, messages []*Message, options ...Option) {
	var (
		toAddr []*mail.Address
		err    error
		ps     = newParams(*c.user)
		buffer = bytes.NewBuffer(nil)
		w      = multipart.NewWriter(buffer)
	)
	options = append(options, withBoundary(w.Boundary()))
	for _, option := range options {
		option(ps)
	}
	defer func(err *error) {
		if err != nil && *err != nil && ps.callback != nil {
			ps.callback(ps.key, ps.addresses, buffer.Bytes(), *err)
		}
		_ = w.Close()
	}(&err)
	if toAddr, err = mail.ParseAddressList(to); err != nil {
		return
	}
	ps.addresses = addressSlice(toAddr).Addr()
	ps.headers[headerTo] = addressSlice(toAddr).String()
	ps.writeHeaders(buffer)
	for _, message := range messages {
		iw, e := w.CreatePart(message.h)
		if e == nil {
			if _, e = iw.Write([]byte(message.c)); e == nil {
				_, e = iw.Write(crlf)
			}
		}
		if e != nil {
			buffer.Reset()
			err = fmt.Errorf("Write email message error %s ", e.Error())
			return
		}
	}
	if c.Closed() {
		err = connectionClosedErr
	} else {
		c.queue <- newLetter(buffer.Bytes(), ps)
	}
}

func (c *Client) SendLetter(letter *letter) {
	c.send(letter)
}

func (c *Client) waitForLetter(ctx context.Context) {
	atomic.StoreInt32(&c.isRunning, 1)
	defer func() {
		atomic.StoreInt32(&c.isRunning, 0)
		for {
			select {
			case _letter := <-c.queue:
				c.send(_letter)
			default:
				c.stopped <- struct{}{}
				return
			}
		}
	}()
	for {
		select {
		case _letter := <-c.queue:
			c.send(_letter)
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) send(_letter *letter) {
	client, err := c.smtpClient()
	if err == nil {
		for _, addr := range _letter.addresses {
			if err = client.Rcpt(addr); err != nil {
				break
			}
		}
	}
	if err == nil {
		var writer io.WriteCloser
		if writer, err = client.Data(); err == nil {
			if _, err = writer.Write(_letter.content); err == nil {
				if err = writer.Close(); err == nil {
					_ = client.Quit()
				}
			}
		}
	}
	if _letter.callback != nil {
		_letter.callback(_letter.key, _letter.addresses, _letter.content, err)
	}
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
