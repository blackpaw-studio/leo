# leo completion

Generate a shell completion script for Leo.

## Usage

```bash
leo completion bash|zsh|fish
```

Task, process, and template names support tab-completion across the CLI once completions are installed.

## Loading Completions

**bash:**

```bash
source <(leo completion bash)
```

Add to `~/.bashrc` to load on every shell.

**zsh:**

```bash
echo 'source <(leo completion zsh)' >> ~/.zshrc
```

If you see `complete:13: command not found: compdef`, enable completion first with `autoload -U compinit && compinit`.

**fish:**

```bash
leo completion fish | source
```

To persist:

```bash
leo completion fish > ~/.config/fish/completions/leo.fish
```
