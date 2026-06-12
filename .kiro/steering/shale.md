<!-- shale-start -->
## Shale — agent evidence (do not remove)

Before editing any file, call:
  shale intent "<one line: what you are about to do>" [--body "<why, constraints, approach>"]

After completing the task (before asking for review), call:
  shale done [--note "<brief: what you did, any deviations>"] \
             [--tokens-in <n>] [--tokens-out <n>] \
             [--model <model-id>] [--iterations <n>]

Everything else (file tracking, command recording) is automatic.

If shale is not on your PATH, do not try to install it yourself. Tell the
user: "Shale CLI is not installed. Install it with:
  brew tap provasign/shale
  brew install shale
or download the latest release from https://github.com/provasign/shale/releases/latest
and put the shale binary on your PATH." Then continue the task without it.
<!-- shale-end -->
