# Install Docker

MediaMTX UI runs in Docker, with Docker Compose starting its two containers from one file. This page takes you from a
fresh machine to a working Docker: opening a terminal on the server, installing Docker Engine and the Compose plugin
with one command (or step by step on Ubuntu, Debian, Raspberry Pi OS or Fedora/RHEL), and the few steps after. If `docker compose version` already
answers on your machine, you can skip to [Install](install.md).

**Docker** runs programs in *containers*: each one comes with everything it needs, so you do not install MediaMTX or
its dependencies on the system yourself. **Docker Compose** reads a file (`compose.yaml`) that describes several
containers and how they fit together, and starts them all with one command.

## Open a terminal on the server

Everything on this page and in [Install](install.md) is typed into a terminal (a command line) on the server.

- **Sitting at the machine** (a Raspberry Pi with a screen, a Linux desktop): open the "Terminal" application.
- **A server somewhere else** (a cloud server, a Pi without a screen): connect to it with **SSH**, which gives you a
  terminal on the server from your own computer. Windows 10 and 11 (PowerShell or Windows Terminal), macOS (Terminal)
  and Linux all have the `ssh` command built in.

To connect, use the username and address your provider gave you (for a Raspberry Pi, the ones you chose in Raspberry
Pi Imager, where you also switch SSH on):

```bash
ssh youruser@203.0.113.10
```

The first time, SSH asks whether you trust the server's fingerprint: type `yes`. Then enter the password (nothing
appears while you type; that is normal). When the prompt changes to something like `youruser@server:~$`, you are on
the server. `exit` ends the session.

Many commands below start with `sudo`, which runs them as the administrator (root). It asks for your own password
the first time.

## Install Docker with one command

On Ubuntu, Debian, Raspberry Pi OS, Fedora and most other Linux systems, Docker's own install script does everything
for you: it finds out which system you have, adds Docker's package repository, and installs Docker Engine with the
Compose plugin. Download it:

```bash
curl -fsSL https://get.docker.com -o get-docker.sh
```

(If the shell says `curl: command not found`: `sudo apt install curl` on Ubuntu, Debian and Raspberry Pi OS,
`sudo dnf install curl` on Fedora.)

If you like, see what it would do first, without changing anything:

```bash
sudo sh ./get-docker.sh --dry-run
```

Then install:

```bash
sudo sh ./get-docker.sh
```

It takes a minute or two and ends with a few lines about using Docker as a non-root user. Continue with
[After installing](#after-installing).

> [!NOTE]
> The script is meant for a fresh machine; run it once. Because it adds Docker's repository, Docker then updates
> with the rest of the system (`sudo apt upgrade` or `sudo dnf upgrade`). Docker itself prefers the step-by-step way
> below for production servers, mainly so that you see each change it makes; the result is the same packages.

## Step by step instead

If you prefer to install from Docker's repository by hand (or the script does not support your system), follow the
steps for your system:

| Your system | Use |
|---|---|
| Ubuntu 22.04, 24.04, 26.04 | [Ubuntu](#ubuntu) |
| Debian 12 (bookworm), 13 (trixie) | [Debian and Raspberry Pi OS](#debian-and-raspberry-pi-os) |
| Raspberry Pi OS, 64-bit | [Debian and Raspberry Pi OS](#debian-and-raspberry-pi-os) |
| Fedora, RHEL | [Fedora and RHEL](#fedora-and-rhel) |
| Windows or macOS, to try it on your own computer | [Docker Desktop](#windows-and-macos-docker-desktop) |

Either way, install Docker from Docker's own repository rather than your distribution's `docker.io` or
`docker-compose` packages: those are often older, and MediaMTX UI's compose files need a current Compose plugin
(`docker compose`, with a space).

These steps follow Docker's official instructions as of this writing. If a command fails, compare with Docker's page
for your system: [docs.docker.com/engine/install](https://docs.docker.com/engine/install/).

## Ubuntu

**1. Remove old or unofficial Docker packages**, if any (it is fine if this says none are installed):

```bash
sudo apt remove $(dpkg --get-selections docker.io docker-compose docker-compose-v2 docker-doc docker-buildx podman-docker containerd runc | cut -f1)
```

**2. Add Docker's repository.** These commands install the tools apt needs, download Docker's signing key (so apt can
check that the packages really come from Docker), and tell apt where Docker's packages are:

```bash
sudo apt update
```

```bash
sudo apt install ca-certificates curl
```

```bash
sudo install -m 0755 -d /etc/apt/keyrings
```

```bash
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
```

```bash
sudo chmod a+r /etc/apt/keyrings/docker.asc
```

Then paste this whole block at once (it writes one file, `/etc/apt/sources.list.d/docker.sources`):

```bash
sudo tee /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
```

```bash
sudo apt update
```

**3. Install Docker Engine and the Compose plugin:**

```bash
sudo apt install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Answer `Y` when apt asks. Docker starts by itself after the install. Continue with [After installing](#after-installing).

## Debian and Raspberry Pi OS

The same steps as for Ubuntu, with Debian's repository. They are also the right ones for **Raspberry Pi OS 64-bit**,
which Docker supports through its Debian packages.

> [!NOTE]
> On a Raspberry Pi, use the 64-bit Raspberry Pi OS (choose it in Raspberry Pi Imager). Docker has announced that
> Docker Engine 28 is the last version for the 32-bit Raspberry Pi OS, and boards with an ARMv6 processor (Pi 1, Pi
> Zero and Zero W) are not supported at all. Check with `uname -m`: `aarch64` means 64-bit.

**1. Remove old packages**, if any:

```bash
sudo apt remove $(dpkg --get-selections docker.io docker-compose docker-doc docker-buildx podman-docker containerd runc | cut -f1)
```

**2. Add Docker's repository:**

```bash
sudo apt update
```

```bash
sudo apt install ca-certificates curl
```

```bash
sudo install -m 0755 -d /etc/apt/keyrings
```

```bash
sudo curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
```

```bash
sudo chmod a+r /etc/apt/keyrings/docker.asc
```

Paste this whole block at once:

```bash
sudo tee /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/debian
Suites: $(. /etc/os-release && echo "$VERSION_CODENAME")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
```

```bash
sudo apt update
```

**3. Install Docker Engine and the Compose plugin:**

```bash
sudo apt install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Continue with [After installing](#after-installing).

## Fedora and RHEL

On **Fedora**, add Docker's repository and install:

```bash
sudo dnf config-manager addrepo --from-repofile https://download.docker.com/linux/fedora/docker-ce.repo
```

```bash
sudo dnf install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

On **RHEL** 8, 9 or 10:

```bash
sudo dnf -y install dnf-plugins-core
```

```bash
sudo dnf config-manager --add-repo https://download.docker.com/linux/rhel/docker-ce.repo
```

```bash
sudo dnf install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

dnf may ask you to accept Docker's GPG key; check that the fingerprint matches the one on Docker's
[Fedora](https://docs.docker.com/engine/install/fedora/) or [RHEL](https://docs.docker.com/engine/install/rhel/) page.
On this family, Docker does not start by itself after the install: start it now and on every boot:

```bash
sudo systemctl enable --now docker
```

For CentOS Stream, see Docker's [CentOS page](https://docs.docker.com/engine/install/centos/).

## After installing

### Start Docker on boot

On Ubuntu, Debian and Raspberry Pi OS, Docker starts on boot already. To make sure (it does no harm):

```bash
sudo systemctl enable docker.service containerd.service
```

MediaMTX UI's containers are set to `restart: unless-stopped`, so once Docker runs at boot, they come back by
themselves after a reboot or a power cut.

### Use docker without sudo

By default only root may talk to Docker, so every `docker` command needs `sudo` in front. To use Docker as your own
user, add yourself to the `docker` group:

```bash
sudo usermod -aG docker $USER
```

Group changes apply to new logins only: **log out and back in** (with SSH, `exit` and connect again). Then
`docker ps` works without `sudo`. (If the command says the `docker` group does not exist, create it with
`sudo groupadd docker` first.)

> [!WARNING]
> Being in the `docker` group is as powerful as being root: whoever can run `docker` can take over the whole machine.
> Only add accounts you would also trust with `sudo`. If you prefer, skip this step and put `sudo` in front of every
> `docker` command in this wiki instead.

### Check that it works

(If you skipped the `docker` group, put `sudo` in front of these commands.)

```bash
docker run hello-world
```

Docker downloads a tiny test image and runs it. It worked when you see, among other lines:

```
Hello from Docker!
This message shows that your installation appears to be working correctly.
```

Then check the Compose plugin:

```bash
docker compose version
```

It prints something like `Docker Compose version v2.40.0`. (If it says `docker: 'compose' is not a docker command`,
the Compose plugin is missing: install `docker-compose-plugin` as above. The old `docker-compose` command, with a
hyphen, is not the same and is not supported here.)

You can remove the test container and image again:

```bash
docker rm $(docker ps -aq --filter ancestor=hello-world)
```

```bash
docker image rm hello-world
```

You are ready for [Install](install.md) (or [Local network only](install-local.md)).

## Windows and macOS: Docker Desktop

On your own Windows or Mac computer, [Docker Desktop](https://docs.docker.com/desktop/) is the easy way to try
MediaMTX UI: install it, start it, and use its terminal (or PowerShell / Terminal) for the `docker compose` commands.
It includes the Compose plugin.

Docker Desktop is fine for trying things out in [local mode](install-local.md#try-it-on-your-own-computer) on that
one computer. It is not meant for a server: it runs the containers in a Linux virtual machine in the background, only while you
are signed in and the computer is awake, with an extra layer of networking between them and your network. For anything other
people rely on, use a Linux machine.

On Windows, Docker Desktop uses **WSL 2** (the Windows Subsystem for Linux) and sets it up during the install if
needed. You can also open a WSL terminal (for example Ubuntu from the Microsoft Store) and run the commands from this
wiki there, as on Linux, while Docker Desktop provides Docker.

## Next

- [Before you start](before-you-start.md): domain, ports and firewall, if you have not planned them yet.
- [Install](install.md): the public install with HTTPS.
- [Local network only](install-local.md): an install for your own network.
