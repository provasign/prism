# Engine benchmark: change-impact completeness

Engine-only track (MKT-03): no model is involved. For each case, change-impact's sites are scored against
committed ground truth: every site the merged upstream change had to touch. Recall is the share of
required sites reported. Precision is the share of reported non-test sites that are required; sites in
test files are neutral (harness/scoring/score.py).

- prism commit: `3845bd3`
- invariant suite: `provasign/research` at `8dfe92928048`, `harness/scoring/ci_invariants.py`
- result: all invariants held

## Per language

| Language | Cases | Required sites | Recall (site-weighted) | Precision (mean) |
|---|---|---|---|---|
| Go | 3 | 125 | 125/125 = 100.0% | 100.0% |
| Java | 8 | 680 | 680/680 = 100.0% | 100.0% |
| Python | 1 | 35 | 33/35 = 94.3% | 100.0% |
| TypeScript | 1 | 37 | 37/37 = 100.0% | 100.0% |

## Per case

| Case | Language | Required sites | Recall | Precision | Sites reported |
|---|---|---|---|---|---|
| jackson-jsonnode-get | Java | 8 | 1.0000 | 1.0000 | 23 |
| jackson-settable-set | Java | 22 | 1.0000 | 1.0000 | 24 |
| jackson-writetypeprefix | Java | 38 | 1.0000 | 1.0000 | 42 |
| jackson-serializewithtype | Java | 58 | 1.0000 | 1.0000 | 61 |
| jackson-deserialize | Java | 104 | 1.0000 | 1.0000 | 223 |
| jackson-serialize | Java | 108 | 1.0000 | 1.0000 | 197 |
| typeorm-driver-escape | TypeScript | 37 | 1.0000 | 1.0000 | 37 |
| django-quotename | Python | 35 | 0.9429 | 1.0000 | 38 |
| guava-forwarding-delegate | Java | 325 | 1.0000 | 1.0000 | 374 |
| commons-collections-transformer-transform | Java | 17 | 1.0000 | 1.0000 | 49 |
| grafana-122750 | Go | 16 | 1.0000 | 1.0000 | 28 |
| grafana-120119 | Go | 15 | 1.0000 | 1.0000 | 16 |
| grafana-bigblast-txsession | Go | 94 | 1.0000 | 1.0000 | 103 |

Recall below 1.0 is a real miss; it is reported, not hidden. "Sites reported" counts every site
change-impact returned, including test-file sites (neutral in scoring) and a site reached through more
than one group (a declaration that is also a family member), which scoring counts once.

## Structural invariants

**Family-size invariant (declaration + overrides == oracle count)**

- gson TypeAdapter.write src/main/ declarations+family=55 (oracle 55): OK

**Compile invariant (missing-implementations == 0, modulo documented exceptions)**

- jackson-databind JsonSerializer.serialize missing=0 (documented=0): OK
- gin ResponseWriter.Status missing=0 (documented=0): OK
- typeorm Driver.escape missing=0 (documented=0): OK
- django BaseDatabaseOperations.quote_name missing=1 (documented=1): OK

**Rename-plan completeness (contract declaration never Unresolved)**

- grafana-122750 DataKeyCache.GetById unresolved=0: OK
- typeorm Driver.escape unresolved=0: OK

**Determinism (double cold index, byte-identical counts)**

- gin: pass1=(2161, 11590) pass2=(2161, 11590): OK

**Decoy invariant (comments must not change the graph)**

- skipped: no --decoy-injector (go install github.com/provasign/astkit/cmd/decoy@<tag>): SKIPPED

## Corpus

| Repository | Commit | Source |
|---|---|---|
| jackson-databind | `0b422144d178` | https://github.com/FasterXML/jackson-databind/archive/0b422144d1785200e44a0b00c973f6ac95adcf5a.tar.gz |
| typeorm | `3d55188c0dd1` | https://github.com/typeorm/typeorm/archive/3d55188c0dd1256f520143379ecf97f45e71acba.tar.gz |
| django | `318a316a4c86` | https://github.com/django/django/archive/318a316a4c86a65bede68144f9546a6056d91379.tar.gz |
| guava | `f06690fa3e87` | https://github.com/google/guava/archive/f06690fa3e874f65515e8fd338a74d636e2c792f.tar.gz |
| commons-collections | `4db4327796f9` | https://github.com/apache/commons-collections/archive/4db4327796f9679b9e57f6788d1d7bb2e8459360.tar.gz |
| grafana-122750 | `b6fdc12f22de` | https://github.com/grafana/grafana/archive/b6fdc12f22de961e1cb5c233512df675d2d4e32b.tar.gz |
| grafana-120119 | `cea7eb61f432` | https://github.com/grafana/grafana/archive/cea7eb61f4321738a895095942d600bd770b51a8.tar.gz |
| gson | `dae37cf0fe12` | https://github.com/google/gson/archive/dae37cf0fe12235b76fb09f01118a0a8c8823f42.tar.gz |
| gin | `d75fcd4c9ab2` | https://github.com/gin-gonic/gin/archive/d75fcd4c9ab260e5225de590f1f0f8c0e0e12d11.tar.gz |

## Reproduce

```sh
git -C research checkout 8dfe92928048
python3 harness/scoring/ci_invariants.py --corpus-root /tmp/ci-corpus --prism ./prism \
  --decoy-injector $(go env GOPATH)/bin/decoy > ci.log
python3 scripts/engine_benchmark.py ci.log --prism-commit 3845bd3 --research-commit 8dfe92928048
```

Use a fresh corpus root: the harness treats any existing corpus directory as already fetched.
