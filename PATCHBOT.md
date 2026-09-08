# patchbot — sub2API 薄补丁层自动重放

上游 `Wei-Shaw/sub2api` 是主干；本仓库只维护一个 `patches` 分支：**上游最新 tag + 少量自有补丁提交**。
`.github/workflows/patchbot.yml` 每 30 分钟检查一次上游有没有新 tag，有就把补丁重放到新 tag 上、过编译与单测门禁、出镜像到 GHCR。

```
upstream tag vX.Y.Z ──rebase --onto──> patches (vX.Y.Z + 补丁) ──go build / unit test──> ghcr.io/atogumo/sub2api:vX.Y.Z (+ :latest)
                                                                                        └─ git tag patched/vX.Y.Z  （"已处理"标记）
```

## 分支与标记

| 对象 | 含义 |
|---|---|
| `patches` 分支（默认分支） | 上游 tag + 补丁提交 + 本工作流文件。**只在这里改东西** |
| `patched/<tag>` 标签 | 该上游 tag 已成功处理。存在则跳过，删掉可重跑 |
| `ghcr.io/atogumo/sub2api:<tag>` | 对应上游 tag 的打补丁镜像；`:latest` 指向最近一次成功 |
| `fix/*` 分支 | 只用于向上游提 PR，与流水线无关 |

补丁基线不用记录：`merge-base(patches, upstream/main)` 就是当前基线，rebase 用 `--onto <tag> <基线>` 只重放补丁提交。
上游 tag 早于基线时（回退）自动跳过。

## 首次启用（一次性）

1. 本地把补丁分支整理到最新上游 tag 上，加入本文件与工作流，作为 `patches` 分支推到 fork。
2. 仓库 Settings → General → Default branch 改为 `patches`（GitHub 只在默认分支上运行 schedule）。
3. Settings → Actions → General：Workflow permissions 选 **Read and write**（工作流要推分支、推标签、开 issue、推镜像）。
4. Actions 页手动运行一次 `patchbot`（workflow_dispatch，参数留空），看它把当前 tag 构建出来。
5. 首次推镜像后到 Packages 页确认 `sub2api` 包可见性为 Public，否则 VPS 拉取要登录。

## 日常

**什么都不用做。** 上游发 tag 后 30 分钟内新镜像出现在 GHCR。

**升级生产**：看完上游 release note 后，在 VPS 上

```bash
# compose 里 image 固定写具体 tag，不写 latest，升级是一次有意识的动作
sed -i 's#image: ghcr.io/atogumo/sub2api:.*#image: ghcr.io/atogumo/sub2api:vX.Y.Z#' docker-compose.yml
docker compose pull sub2api && docker compose up -d sub2api
```

回滚 = 把 tag 改回上一个，再 `pull && up -d`。

**新增补丁**：在本地 `patches` 分支上提交（小、独立、一个问题一个提交），`git push`。下次上游发 tag 时自动带上。
每个补丁同时向上游提 PR；上游合并后，rebase 时该提交会变成空提交被自动丢弃，补丁集只会变薄。

## 冲突时（唯一需要人的时刻）

工作流失败并开一个带 `patchbot` 标签的 issue，列出冲突文件。本地：

```bash
git fetch upstream --tags
git checkout patches
git rebase --onto <tag> $(git merge-base patches upstream/main)
# 解决冲突 → git add → git rebase --continue
cd backend && go build ./... && go test -tags=unit ./internal/handler/
git push --force-with-lease origin patches
```

推完不用手动触发，下一次轮询会重试并关闭流程；issue 手动关掉即可。

## 边界与已知限制

- GitHub 不提供"别人仓库发 tag"的事件，30 分钟轮询是等价替代；轮询空转只花十几秒。
- 仓库 60 天无活动会被 GitHub 关掉定时任务；每次成功都会推 `patched/*` 标签，正常运行不会触发。
- 只构建 `linux/amd64`。需要 arm64 时在 `platforms` 加 `linux/arm64`，构建时间约翻倍。
- 门禁只跑 `go build ./...` 和 handler 包单测；上游行为回归不在门禁范围内，升级前仍要看 release note。
- 提交身份固定为 `atogumo <41533924+atogumo@users.noreply.github.com>`，rebase 产生的 committer 也是它。
