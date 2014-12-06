FROM ubuntu
RUN apt-get update -y && apt-get install --no-install-recommends -y -q curl build-essential ca-certificates git mercurial bzr
RUN mkdir /goroot && curl https://storage.googleapis.com/golang/go1.3.3.linux-amd64.tar.gz | tar xvzf - -C /goroot --strip-components=1
RUN mkdir -p /gopath/bin
RUN mkdir /mailing_list_daemon

ENV GOROOT /goroot
ENV GOPATH /gopath
ENV GOBIN /gopath/bin
ENV PATH $PATH:$GOROOT/bin:$GOPATH/bin

RUN go get github.com/mhale/mailrouter
ADD . /mailing_list_daemon.git
ENV GOPATH $GOPATH:/mailing_list_daemon
#ADD ./mailrouter.conf /etc

EXPOSE 8080
EXPOSE 2525

