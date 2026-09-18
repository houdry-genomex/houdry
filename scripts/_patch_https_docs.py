from pathlib import Path

p = Path(r"C:\Users\abhis\projects\houdry\docs\GPU-discovery.md")
t = p.read_text(encoding="utf-8")
t = t.replace("node join --server http://HOST:18080", "node join --server https://HOST:18080")
p.write_text(t, encoding="utf-8")

p = Path(r"C:\Users\abhis\projects\houdry\README.md")
t = p.read_text(encoding="utf-8")
t = t.replace(
    "Point Agent or an SDK at `http://127.0.0.1:8080/v1`",
    "Point Agent or an SDK at `https://127.0.0.1:8080/v1`",
)
p.write_text(t, encoding="utf-8")

p = Path(r"C:\Users\abhis\projects\houdry\docs\OpenAI-compatible-API.md")
t = p.read_text(encoding="utf-8")
t = t.replace(
    "houdry node join --server http://127.0.0.1:18080",
    "houdry node join --server https://127.0.0.1:18080",
)
t = t.replace(
    "curl http://127.0.0.1:18080/v1/chat/completions",
    'curl --cacert "$HOME/.houdry/server/pki/root_ca.crt" https://127.0.0.1:18080/v1/chat/completions',
)
t = t.replace(
    "OPENAI_API_BASE=http://127.0.0.1:18080/v1",
    "OPENAI_API_BASE=https://127.0.0.1:18080/v1",
)
p.write_text(t, encoding="utf-8")

p = Path(r"C:\Users\abhis\projects\houdry\go.mod")
t = p.read_text(encoding="utf-8")
if "gopkg.in/yaml.v3 v3.0.1" in t and "gopkg.in/yaml.v3 v3.0.1\n)" not in t:
    t = t.replace(
        "\tgolang.org/x/net v0.38.0\n)\n",
        "\tgolang.org/x/net v0.38.0\n\tgopkg.in/yaml.v3 v3.0.1\n)\n",
    )
    t = t.replace("\tgopkg.in/yaml.v3 v3.0.1 // indirect\n", "")
    p.write_text(t, encoding="utf-8")

print("ok")
print(p.read_text(encoding="utf-8"))
