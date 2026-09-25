---
title: Dicer
layout: hextra-home
description: "Run any OCI image as a virtual machine, with a kernel of its own, under Cloud Hypervisor or Firecracker."
---

{{< home/hero
  badge="Open source · MIT licensed"
  badgeLink="https://github.com/konradasb/dicer"
  title="Your container images,"
  highlight="booted as virtual machines."
  subtitle="Dicer boots any OCI image as a virtual machine with a kernel of its own, under Cloud Hypervisor or Firecracker. One daemon, one command, on your own Linux host."
  install="curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash"
>}}
{{< home/terminal title="dicer" >}}
$ dicer run -d --name web -p 8080:80 nginx:1.27
Image docker.io/library/nginx:1.27 pulled in 7.9s (108.8 MiB)
Instance web started in 380ms (172.20.151.177)
$ dicer ps -c name,status,ports
NAME  STATUS        PORTS
web   Up 2 seconds  8080->80/tcp
$ dicer exec web nginx -v
nginx version: nginx/1.27.5
$ curl -sI http://dicer1.example.com:8080 | head -1
HTTP/1.1 200 OK
{{< /home/terminal >}}
{{< /home/hero >}}
