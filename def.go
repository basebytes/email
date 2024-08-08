package email

import (
	"bytes"
	"errors"
	"fmt"
	"net/mail"
	"net/textproto"
	"strings"
)

const (
	mimeVersion             = "1.0"
	encodingBase64          = "base64"
	encodingQuotedPrintable = "quoted-printable"
	contentTypeText         = "text/plain; charset=UTF-8"
	contentTypeHtml         = "text/html; charset=UTF-8"
	contentType8bit         = "application/octet-stream"
	contentTypeMixed        = "multipart/mixed"
	defaultMaxQueueSize     = 100
)

const (
	headerFrom                    = "From"
	headerTo                      = "To"
	headerReturnPath              = "Return-Path"
	headerReplyTo                 = "Reply-To"
	headerCc                      = "Cc"
	headerSubject                 = "Subject"
	headerContentType             = "Content-Type"
	headerContentTransferEncoding = "Content-Transfer-Encoding"
	headerContentDisposition      = "Content-Disposition"
	headerContentID               = "Content-ID"
	headerMimeVersion             = "Mime-Version"
)

var (
	crlf                = []byte{'\r', '\n'}
	kvSeparator         = []byte{':'}
	boundary            = []byte("boundary=")
	filename            = []byte("filename=")
	connectionClosedErr = errors.New("Connection closed ")
)

type Config struct {
	Server       string
	User         string
	Password     string
	MaxQueueSize int
}

type Option func(params *params)

func newMessage(contentType, encoding string, headers map[string][]string, content string) *Message {
	m := &Message{h: textproto.MIMEHeader{}, c: content}
	m.h.Set(headerContentType, contentType)
	m.h.Set(headerContentTransferEncoding, encoding)
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

type Message struct {
	h textproto.MIMEHeader
	c string
}

func (m *Message) header() textproto.MIMEHeader {
	return m.h
}

func (m *Message) content() string {
	return m.c
}

func NewContent() *Content {
	return &Content{}
}

type Content struct {
	Subject string        `gorm:"-" json:"subject,omitempty"`
	Html    string        `gorm:"-" json:"html,omitempty"`
	Att     []*Attachment `gorm:"-" json:"att,omitempty"`
}

func (c *Content) AppendAtt(att *Attachment) {
	c.Att = append(c.Att, att)
}

func (c *Content) AppendAttContent(content []byte) {
	if len(c.Att) == 0 {
		return
	}
	c.Att[len(c.Att)-1].Content = content
}

func NewAttachment(name string) *Attachment {
	return &Attachment{Name: name}
}

type Attachment struct {
	Name    string `json:"name,omitempty"`
	Content []byte `json:"content,omitempty"`
}

type addressSlice []*mail.Address

func (a addressSlice) String() string {
	var addr []string
	for _, add := range a {
		addr = append(addr, add.String())
	}
	return strings.Join(addr, ",")
}

func (a addressSlice) Addr() (addr []string) {
	for _, add := range a {
		addr = append(addr, add.Address)
	}
	return addr
}

func newParams(user mail.Address) *params {
	return &params{
		user: &user,
		headers: map[string]string{
			headerFrom:        user.String(),
			headerMimeVersion: mimeVersion,
		},
	}
}

type params struct {
	user      *mail.Address
	headers   map[string]string
	addresses []string
	key       string
	callback  func(key string, addresses []string, content []byte, err error)
}

func (p *params) Add(k, v string) {
	p.headers[k] = v
}

func (p *params) writeHeaders(buffer *bytes.Buffer) {
	for key, value := range p.headers {
		_, _ = fmt.Fprintf(buffer, "%s:%s%s", key, value, crlf)
	}
	buffer.Write(crlf)
}

func NewLetter(key string, addresses []string, content []byte, callback func(string, []string, []byte, error)) *letter {
	return &letter{key: key, addresses: addresses, content: content, callback: callback}
}

func newLetter(content []byte, params *params) *letter {
	l := &letter{content: content, addresses: params.addresses}
	if params.callback != nil {
		l.callback = params.callback
		l.key = params.key
	}
	return l
}

type letter struct {
	key       string
	addresses []string
	content   []byte
	callback  func(string, []string, []byte, error)
}
