# tram — kế hoạch dự án mới

> Viết lại từ số 0. Không port code cũ. Chỉ giữ **những ý tưởng đã được chứng minh là đúng**
> trong sshm/sshfleet, bỏ hết phần còn lại.

---

## 1. Tên

**`tram`** — từ tiếng Việt *trạm*: trạm trung chuyển, trạm dừng. Đúng nghĩa đen của một
jump host, và một chuỗi `ProxyJump` chính là một **tuyến trạm**.

Vì sao chọn:

- 4 ký tự, gõ hàng ngày không mỏi, không dấu, không nhầm khi gõ nhanh
- đọc được ở cả hai ngôn ngữ (`tram` trong tiếng Anh cũng là phương tiện chở bạn tới nơi)
- không rơi vào rừng tên nautical/k8s đã bão hoà (`fleet`, `harbor`, `keel`, `tiller`,
  `atlas`, `waypoint`, `hop`, `muster` — đều đã có chủ, vài cái ngay trong hệ sinh thái
  cloud-native)
- có sẵn một hệ ẩn dụ nhất quán cho toàn bộ tài liệu: host = **trạm**, ProxyJump chain =
  **tuyến**, group = **khu**

Câu định vị: **`tram` — ssh_config của bạn, có mắt nhìn.**

Dự phòng nếu `tram` vướng: `berth`, `waypost`, `pennant`. Trước khi chốt, kiểm tra ba chỗ:
`pkg.go.dev`, Homebrew formula, và `apt-cache search` — chỉ cần trùng một binary trong PATH
là đủ lý do đổi.

---

## 2. Định nghĩa và phản-định nghĩa

**tram là:** một lớp nhìn và chỉnh sửa `~/.ssh/config` an toàn, cộng với một nắm lệnh chạy
song song trên nhiều host.

**tram không phải là:**

| Không phải | Vì |
|---|---|
| terminal emulator | Terminal của bạn đã tốt hơn bất cứ thứ gì tôi viết được. `tram` giao tay lái cho `ssh` chạy trên terminal thật. |
| kho kết nối riêng | Không có `connections.yaml`. Xoá `tram` đi, `ssh web1` vẫn chạy. Đây là điểm khác biệt lớn nhất so với 90% tool cùng loại. |
| dịch vụ nền | Không daemon, không agent riêng, không port lắng nghe. |
| công cụ đồng bộ | Không vault, không server, không tài khoản. Đồng bộ là việc của git/Syncthing với file `ssh_config` của bạn. |
| SFTP client | `sftp` đã có sẵn. `tram` chỉ mở nó. |
| monitoring | Dashboard là để nhìn nhanh trước khi vào, không phải để thay Grafana. |

Viết bốn dòng này lên đầu README và giữ nguyên trong hai năm.

---

## 3. Chỉ giữ đúng 6 ưu điểm

Đây là toàn bộ những gì được kế thừa. Mọi thứ khác trong sshfleet coi như chưa từng tồn tại.

### ① `ssh_config` là nguồn sự thật duy nhất

Không phải "hỗ trợ import/export ssh_config" — mà **ghi thẳng vào đó, và chỉ đó**. Kèm bộ
bảo đảm:

- `Host *`, `Match`, `Include`, comment của người dùng: giữ verbatim, byte-for-byte
- comment và directive lạ nằm trong một khối Host thì **thuộc về khối đó** (xoá host thì
  xoá cùng, không mồ côi trôi lên đầu file thành cấu hình toàn cục)
- nhiều `IdentityFile` giữ đủ; keyword khai trùng giữ đúng thứ tự (ssh lấy giá trị đầu tiên)
- ghi qua file tạm + `rename`, backup `<file>.tram.bak` trước mỗi lần ghi

### ② Account — danh tính tách khỏi địa chỉ

Một chiều: account **sinh ra** `User`/`IdentityFile` trong từng khối host, không thay thế
chúng. Sửa host bằng tay thì host giữ liên kết nhưng được đánh dấu lệch, và `apply` không
bao giờ đè lên phần bạn vừa sửa.

Account **chỉ giữ danh tính** (user, cách xác thực, đường dẫn key). Địa chỉ, cổng, ProxyJump,
group thuộc về host. Ranh giới này là thứ làm model không phình ra.

### ③ Hiểu ProxyJump thật sự

Không tool nào khác làm đủ bốn thứ này:

- resolve và hiển thị cả tuyến
- xoá một trạm → cảnh báo đúng những host sẽ gãy
- đổi tên trạm → tự sửa `ProxyJump` của mọi host trỏ tới nó
- phát hiện vòng (ssh sẽ **treo**, không báo lỗi) và loại sẵn khỏi picker

### ④ Chẩn đoán phân loại được, không phải "OK/FAIL"

`ping` phân biệt `OK` · `AUTH` · `REFUSED` · `TIMEOUT` · `DNS` · `HOST_KEY`. `doctor` dừng ở
chặng ProxyJump hỏng **đầu tiên** thay vì báo "không kết nối được".

Đây là khác biệt giữa một tool và một script.

### ⑤ Bí mật nằm đúng chỗ

Keyring của OS (Credential Manager · Keychain · Secret Service), dự phòng file mã hoá bằng
khoá thiết bị. Giá trị **không bao giờ** vào `ssh_config`, không qua dòng lệnh, không qua
biến môi trường — chỉ qua `SSH_ASKPASS`. Mật khẩu gắn với **danh tính** (account) chứ không
gắn với từng host. Passphrase được kiểm tra với key **trước khi lưu**.

### ⑥ Số liệu tính đúng như công cụ người ta đối chiếu

Đầu xa chỉ đọc `/proc` và `df`, mọi phép tính làm ở máy local (vì `/bin/sh` đầu xa có thể là
dash/busybox). Và ba công thức phải khớp với thứ người ta mở ra so:

| Chỉ số | Công thức | Khớp với | Cố ý **không** dùng |
|---|---|---|---|
| CPU | 2 mẫu `/proc/stat` cách 0.25s | tốc độ thật | tổng từ lúc boot |
| RAM | `total − free − buffers − (Cached + SReclaimable)` | cột `used` của `free -m` | `total − MemAvailable` (cao hơn ~6 điểm) |
| Disk | `used / (used + available)` | cột `Use%` của `df -h` | `used / size` (gồm 5% reserved của root) |

---

## 4. Bảy ràng buộc thiết kế

Mỗi điều là một ràng buộc **kiểm chứng được**, không phải khẩu hiệu. Nếu một tính năng vi
phạm một điều, tính năng đó bị loại chứ không phải ràng buộc được nới.

1. **Không vẽ nội dung của phiên ssh.** Không PTY, không emulator, không tab, không prefix
   key. `enter` = nhả terminal cho `ssh`.
2. **Không lưu thứ mà `ssh_config` diễn đạt được.** Nếu ssh có directive cho nó, nó nằm
   trong `ssh_config`. State phụ chỉ được phép cho thứ ssh không có khái niệm (thời điểm
   kết nối gần nhất, snippet).
3. **Mọi lệnh ghi đều có `--dry-run`, và in diff trước khi ghi.**
4. **Chỉ đọc theo mặc định với thứ không do tram viết.** Host bạn viết tay được đọc, kết
   nối, exec — nhưng phải `--force` mới ghi được.
5. **Mọi lệnh đều có `-f json` từ ngày đầu**, khoá ASCII ổn định, ô trống là chuỗi rỗng thật.
   Không bolt-on sau.
6. **Không phụ thuộc cgo.** Clipboard, keyring, terminal detect đều dùng công cụ có sẵn của
   OS. Build được bằng một dòng `go build`.
7. **Thất bại phải nói được là thất bại ở đâu.** Không có thông báo "connection failed".

---

## 5. Mô hình dữ liệu

```
~/.ssh/config                 ← nguồn sự thật. tram đọc/ghi.
~/.ssh/config.d/tram.conf     ← file quản lý riêng (sau `tram init`)
~/.ssh/config.*.tram.bak      ← ảnh chụp trước mỗi lần ghi

~/.config/tram/               (hoặc %APPDATA%\tram\)
├── accounts.json             danh tính
├── snippets.json             lệnh hay dùng
├── history.json              thời điểm kết nối gần nhất + Favorites
└── config.toml               tuỳ chọn của tram (chế độ handoff, template terminal)

OS keyring                    mật khẩu + passphrase
```

**Kiểm thử của nguyên tắc số 2:** xoá sạch `~/.config/tram/` → `tram ls` vẫn ra đúng danh
sách host, `tram web1` vẫn kết nối được. Chỉ mất Recent/Favorites/snippet/account link.

---

## 6. Bề mặt lệnh

Thiết kế sạch từ đầu, không kế thừa lịch sử đặt tên của sshfleet.

### Kết nối

```
tram                          mở TUI
tram <host>                   kết nối (khớp mờ; nhiều kết quả thì mở picker)
tram <host> --window          mở ở tab/cửa sổ mới của terminal
tram <host> -- <cmd>          chạy một lệnh rồi thoát
tram sftp <host>              handoff sang sftp
tram args <host>              in argv mà tram sẽ dùng (để nhét vào script khác)
```

### Xem

```
tram ls                       bảng
tram ls --wide                thêm cột Account, dấu * = lệch khỏi account
tram ls --tree                cây group
tram ls <host>                chi tiết + tuyến ProxyJump đã resolve
tram ls -g prod               lọc theo group (gồm group con)
tram ls -s bastion            tìm theo từ khoá
```

### Sửa

```
tram add <name> --addr 10.0.0.1 --account deploy-key --group prod
tram edit <name> --port 2222
tram edit <name> --account ""        gỡ liên kết, giữ nguyên User/IdentityFile
tram clone <src> <dst> --addr ...
tram rm <name>
tram mv <old> <new>                  đổi tên + tự cập nhật ProxyJump trỏ tới
```

Tất cả nhận `--dry-run` và `--diff`.

### Danh tính

```
tram account ls                          kèm số host đang liên kết
tram account add <n> --user u --key ~/.ssh/id_ed25519
tram account add <n> --user u --auth password|agent
tram account edit <n> --user ops --apply
tram account apply <n> [--dry-run]
tram account rm <n> [--force]

tram secret set <host|account>           mật khẩu → keyring
tram secret ls
tram secret rm <host|account>

tram key push -g prod                    ssh-copy-id có group, idempotent
tram key load <host|path> [-t 8h]        nạp key vào ssh-agent
tram key unload <host|path>
tram key passphrase set|ls|rm <host|path>
```

### Fleet

```
tram exec -g prod -- uptime              song song, mặc định -P 5
tram exec --all --stream -- df -h
tram ping -g prod                        OK/AUTH/REFUSED/TIMEOUT/DNS/HOST_KEY
tram doctor <host>                       dừng ở chặng hỏng đầu tiên
tram doctor --config                     kiểm tra chính ssh_config
tram top [-g prod] [-f json]             dashboard fleet
tram run <snippet> -g prod               chạy snippet
tram tmux -g prod                        N pane + synchronize-panes on
```

`tram doctor --config` là lệnh **mới**, không có trong sshfleet, và đáng có: quyền file
`0600`, thứ tự `Include`, host trùng tên giữa các file, ProxyJump trỏ tới tên không tồn tại,
vòng ProxyJump, `IdentityFile` trỏ tới file không tồn tại, key không có quyền đúng.

### Nhập & thiết lập

```
tram import <file> [--group g] [--dry-run] [--overwrite]
                                         CSV · Ansible INI · Ansible YAML
tram init                                tách file quản lý riêng + chèn Include lên đầu
tram completion bash|zsh|powershell|fish
```

Chuẩn chung: chạy trên >1 host thì hỏi xác nhận (`-y` bỏ qua); có host lỗi thì exit code ≠ 0.

---

## 7. TUI — ba màn hình, hết

Không tab, không split, không prefix, không panel. Ba màn hình và các hộp thoại của chúng.

### Màn hình 1 — Danh sách (mặc định)

| Phím | Việc |
|---|---|
| `enter` | kết nối (nhả terminal cho ssh) |
| `shift+enter` | kết nối ở cửa sổ/tab mới |
| `/` | tìm (`#tag` để lọc), `space` đánh dấu nhiều host |
| `a` `e` `d` `c` | thêm · sửa · xoá · clone |
| `E` | sửa hàng loạt các host đã đánh dấu |
| `A` | quản lý account |
| `p` `x` `D` | ping · chạy lệnh · doctor |
| `r` | palette snippet |
| `M` | dashboard |
| `I` | import |
| `f` | sftp |
| `*` | ghim ⭐ |
| `?` | bảng phím |

Trong form tạo host: **Account · Group · ProxyJump** mở danh sách để chọn, có mục `＋ tạo
mới`. Chọn account rồi thì ô User/Key **biến mất** và một dòng cho biết account sẽ ghi gì.

### Màn hình 2 — Kết quả (ping · exec · doctor · snippet)

Một viewport có thể cuộn, mỗi host một khối gập được, `enter` để kết nối tới host đang chọn.
Đây là chỗ output của snippet non-interactive hiện ra.

### Màn hình 3 — Dashboard (`M`)

Bảng tất cả host, làm mới 5 giây, `enter` kết nối thẳng, `r` làm mới ngay.

**Ranh giới cần giữ:** TUI chỉ vẽ **dữ liệu của chính tram**. Không bao giờ vẽ output của
một chương trình tương tác đầu xa.

---

## 8. Checklist tính năng

**P0** = không có thì tool vô dụng · **P1** = lý do người ta chọn tram thay vì sửa tay ·
**P2** = sau khi P0/P1 ổn định · **Không làm** = ghi ra để chống scope creep.

### P0 — Lõi

- [ ] Parser/writer `ssh_config`: verbatim, atomic write, backup
- [ ] Đọc host qua mọi `Include` (kể cả glob), ghi trả về **đúng file chứa host đó**
- [ ] Bộ test differential `ssh -G` (mục 11) — làm **cùng lúc** với parser, không để sau
- [ ] `ls` (bảng · `--wide` · `--tree` · chi tiết)
- [ ] `add` / `edit` / `clone` / `rm` / `mv` + `--dry-run` + `--diff`
- [ ] Handoff in-place: TUI → ssh → về TUI sạch màn hình, đọc đúng exit code
- [ ] TUI danh sách + tìm kiếm + form
- [ ] `-f json` cho `ls`
- [ ] `tram --version`

### P1 — Lý do tồn tại

- [ ] Account đầy đủ: add/ls/edit/apply/rm, materialize, phát hiện lệch, tôn trọng chỉ-đọc
- [ ] `init` + `Include` chèn lên đầu + khoá chỉ-đọc cho host ngoài file quản lý
- [ ] Group phân cấp, lọc gồm group con
- [ ] ProxyJump: resolve tuyến · cảnh báo khi xoá trạm · cascade khi `mv` · phát hiện vòng ·
      picker loại sẵn host tạo vòng
- [ ] `exec` song song (`-P`, `--stream`, xác nhận >1 host, exit code cho CI)
- [ ] `ping` phân loại lỗi
- [ ] `doctor <host>` theo chặng + `doctor --config`
- [ ] `secret set/ls/rm` + chế độ askpass trong binary (chỉ trả lời 2 câu hỏi, **luôn từ
      chối** xác nhận host key)
- [ ] `key load` / `key passphrase` + TUI hỏi trước khi mở phiên tới host dùng key khoá
- [ ] `key push -g`
- [ ] `import` 3 định dạng + preview trong TUI + `--dry-run` + `--overwrite`
- [ ] Batch edit `E`
- [ ] `-f json|yaml|csv|value` cho mọi lệnh đọc
- [ ] Completion
- [ ] Recent 🕒 + Favorites ⭐

### P2

- [ ] `--window`: Windows Terminal · tmux · macOS · Linux + template trong `config.toml`
- [ ] `tram tmux -g` (thay thế broadcast)
- [ ] Snippets + palette `r` + cờ `--confirm` với ⚠ + bộ mẫu lần đầu
- [ ] `tram top` + dashboard `M`
- [ ] `tram sftp` handoff
- [ ] `tram args` (cho script)
- [ ] Fallback ASCII cho TUI trên console cũ
- [ ] Dán `ctrl+v` vào ô nhập của TUI

### Không làm

- [ ] ~~Bất cứ thứ gì cần vẽ lại nội dung phiên ssh~~
- [ ] ~~Tab / split / prefix key / scrollback / copy chuột trong phiên~~
- [ ] ~~Đồng bộ, vault, tài khoản, server-side~~
- [ ] ~~SFTP browser tự vẽ~~
- [ ] ~~Telemetry realtime trong phiên~~
- [ ] ~~Ghi phiên ra file~~ (dùng `script` hoặc tmux `pipe-pane`)
- [ ] ~~Tunnel manager~~ — cân nhắc lại ở v2 nếu thật sự dùng tới; `ssh -N -L` chạy nền có
      vấn đề vòng đời riêng trên Windows, không đáng đưa vào v1

---

## 9. Kiến trúc

```
main.go                  entrypoint + chế độ askpass (khi được ssh gọi lại)
cmd/                     cobra: mỗi file một lệnh, không chứa logic

internal/
├── sshconf/             parser · writer · atomic+backup · Include resolver
│                        ⚠ package quan trọng nhất. Không import gì của tram.
├── model/               Host, Account, Group, JumpChain — kiểu thuần, không I/O
├── account/             materialize · drift · unlink · store
├── store/               accounts/snippets/history JSON + đường dẫn theo OS
├── secret/              keyring + fallback mã hoá + askpass protocol
├── probe/               ★ một đường code chung cho ping · doctor · top
│                        (phân loại lỗi ssh nằm ở đây, không lặp lại 3 nơi)
├── runner/              chạy song song, timeout, gom output
├── launcher/            dựng argv · handoff in-place · handoff cửa sổ mới · detect terminal
├── importer/            CSV · Ansible INI · Ansible YAML
├── tui/                 list · form · picker · result · dashboard · import
└── render/              bảng + json/yaml/csv/value
```

Hai quyết định kiến trúc đáng ghi lại:

- **`probe` là một package chung.** Ở sshfleet, ping/doctor/top là ba đường code riêng nên
  phân loại lỗi lệch nhau. Ở đây một chỗ định nghĩa `Result{Class, Hop, RawErr}`.
- **`sshconf` không biết gì về tram.** Nó chỉ biết `ssh_config`. Nhờ vậy test được độc lập
  và về sau tách ra thành thư viện riêng được.

**Thứ tự build:** `sshconf` → `model` → `store`/`account` → `cmd/ls` → `launcher` → `tui` →
`probe`/`runner` → phần còn lại. Không viết TUI trước khi `sshconf` xanh hết test.

---

## 10. Milestone

### M0 — Hai thứ khó nhất, làm trước

Chỉ hai việc, nhưng đây là chỗ dự án sống hoặc chết.

**(a) `sshconf` + bộ test differential**

- [ ] Round-trip: đọc → ghi lại → file giống hệt byte-for-byte
- [ ] Corpus 20+ file `ssh_config` thật (có `Match`, `Include` glob, comment, keyword trùng,
      thụt lề lạ, CRLF, không newline cuối file)
- [ ] Với mỗi file: `ssh -G <host>` cho **mọi** host, trước và sau khi tram ghi lại → diff rỗng
- [ ] Fuzz: sinh config ngẫu nhiên, round-trip, không panic, không mất dòng

**(b) Handoff**

- [ ] `tram <host>` nhả terminal, ssh chạy, thoát, quay về sạch
- [ ] `vim` + resize giữa chừng · `htop` + chuột · `tmux` đầu xa (`ctrl+b` không bị chiếm)
- [ ] `ctrl+c` `ctrl+r` `ctrl+w` F10 xuống thẳng shell
- [ ] CJK/emoji không lệch cột
- [ ] Đọc đúng exit code (255 = lỗi ssh, còn lại = lệnh từ xa)
- [ ] `ctrl+z` không treo tram
- [ ] Chạy được trên: Windows Terminal · cmd.exe · VS Code terminal · macOS Terminal ·
      gnome-terminal · bên trong tmux

Không viết dòng TUI nào trước khi M0 xanh.

### M1 — Đọc

`ls` đủ 4 dạng, `-f json`, TUI danh sách + tìm kiếm, `enter` kết nối, `history.json`.
**Mốc dùng được thật:** tự dùng thay `ssh` trong một tuần.

### M2 — Ghi

`add`/`edit`/`clone`/`rm`/`mv` + form TUI + `--dry-run`/`--diff` + backup + khoá chỉ-đọc +
`init`. **DoD:** dùng tram sửa `~/.ssh/config` thật của mình một tuần, không mất dòng nào.

### M3 — Danh tính

Account + secret + askpass + key load/passphrase.
**Rủi ro số 1 nằm ở đây** — xem mục 12.1. Verify askpass **ngay đầu M3**, không để cuối.

### M4 — Tuyến

ProxyJump đầy đủ: resolve, cascade khi `mv`, cảnh báo khi `rm`, phát hiện vòng, picker.
`doctor` theo chặng + `doctor --config`.

### M5 — Fleet

`probe` · `ping` · `exec` · `key push` · màn hình kết quả.

### M6 — Nhập liệu

`import` 3 định dạng + preview + batch edit.

### M7 — Phần còn lại

P2: `--window`, `tram tmux`, snippets, `top`, `sftp`, completion, ASCII fallback.

### M8 — Phát hành

README (4 dòng "không phải là" ở đầu), `goreleaser`, binary cho 5 nền tảng, demo GIF.

---

## 11. Chiến lược test

Ba tầng, tầng đầu là thứ đáng giá nhất và hiếm dự án nào làm:

**① Differential `ssh -G`** — nguồn sự thật để so là **chính ssh**, không phải kỳ vọng của
tôi. Với mỗi file trong corpus và mỗi host trong đó: `ssh -G host` trước khi tram ghi lại,
`ssh -G host` sau khi ghi lại, diff phải rỗng. Đây là bằng chứng "cấu hình hiệu lực không
đổi" — mạnh hơn mọi unit test về parser.

**② Golden file** cho writer: mỗi thao tác (thêm host, xoá host có comment lạ, đổi tên có
cascade) có một cặp before/after được commit vào repo.

**③ Test tích hợp trên container** — một `sshd` trong Docker với 3 host (trực tiếp, qua 1
bastion, qua 2 bastion), dùng cho `ping`/`doctor`/`exec`/`probe`. Thêm một host cố tình
`REFUSED`, một host sai key để test phân loại `AUTH`.

Ma trận terminal ở M0 điền tay, kèm vào release notes.

---

## 12. Rủi ro

### 12.1 `SSH_ASKPASS` khi ssh có tty thật (CAO)

Ở chế độ handoff, ssh có tty thật → nó **ưu tiên hỏi trực tiếp** và bỏ qua askpass, trừ khi
`SSH_ASKPASS_REQUIRE=force` (OpenSSH ≥ 8.4). Trên Windows cần kiểm tra riêng bản OpenSSH
đi kèm có hỗ trợ biến này không.

Nếu không hỗ trợ: tính năng lưu mật khẩu mất giá trị trên Windows. Kế hoạch dự phòng là báo
rõ ràng lúc `secret set` ("ssh của bạn là bản X, không hỗ trợ — hãy dùng key") chứ không im
lặng để nó hỏng vào lúc kết nối.

**Verify việc này ở tuần đầu của M3, trước khi viết phần còn lại.**

### 12.2 Khôi phục console mode trên Windows (CAO)

conhost cũ có thể không được trả về đúng trạng thái sau handoff. Tự lưu/khôi phục bằng
`GetConsoleMode`/`SetConsoleMode` bao quanh, không tin hoàn toàn vào thư viện TUI.

### 12.3 Quoting cho `wt.exe` (TRUNG BÌNH)

`wt.exe` xử lý `;` và `"` rất khó chịu. Giải pháp: **không** nhét argv ssh dài vào `wt`, mà
`wt.exe ... -- tram <name>` — để tiến trình con tự dựng lại lệnh. Chỉ truyền một tên host.

### 12.4 Tự viết parser `ssh_config` (TRUNG BÌNH)

Ngữ pháp có nhiều góc khuất: `Match` với `exec`, `Include` đệ quy, `=` thay vì khoảng trắng,
dấu nháy, token `%h`/`%p`/`%r`. Giảm rủi ro bằng tầng test ① và bằng nguyên tắc: **không
hiểu thì giữ nguyên verbatim**, không cố chuẩn hoá.

### 12.5 Bỏ cuộc giữa chừng (THẬT SỰ CAO)

Đây là dự án viết lại lần thứ ba. Cách chống: M1 và M2 mỗi mốc phải **tự dùng được thật một
tuần** trước khi đi tiếp. Nếu tới M2 mà vẫn không muốn dùng nó thay `ssh`, thì vấn đề không
nằm ở tính năng còn thiếu.

---

## 13. Cần chốt trước khi gõ dòng code đầu tiên

1. **Tên** — `tram` hay một trong ba dự phòng? Kiểm tra collision rồi khoá lại, đổi tên giữa
   chừng tốn hơn bạn nghĩ.
2. **Ngôn ngữ tài liệu và message** — Việt trước rồi dịch Anh, hay Anh trước? Nếu định public
   trên GitHub thì message trong code nên Anh ngay từ đầu.
3. **`tram <host>` khớp mờ hay khớp chính xác?** Khớp mờ tiện nhưng nguy hiểm khi có
   `db-prod` và `db-prod-replica`. Đề xuất: khớp chính xác trước, mờ chỉ khi không có kết quả
   chính xác, và nhiều kết quả thì mở picker chứ không tự đoán.
4. **Có `mv` không?** Cascade `ProxyJump` khi đổi tên là tính năng hay nhưng cũng là chỗ dễ
   phá config nhất. Đề xuất: có, nhưng bắt buộc in diff và hỏi xác nhận.
5. **Bỏ tunnel ở v1?** Đề xuất: bỏ. Xem có nhớ nó không sau ba tháng.
6. **Public hay private repo?** Ảnh hưởng tới mức đầu tư vào README, CI, goreleaser ngay từ
   M0 hay để tới M8.
