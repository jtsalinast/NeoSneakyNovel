# Prueba rápida (Try out)

Binarios de **StorySmith** compilados localmente con la UI web embebida (pestañas: Proyectos · Parámetros de Novela · Configuración API · Outline · Escritura).

## Descargas

| Archivo | Plataforma |
|---|---|
| `storysmith-darwin-arm64.zip` | macOS (Apple Silicon: M1/M2/M3/M4) |
| `storysmith-linux-amd64.zip` | Linux x86-64 |

Verifica la integridad con `SHA256SUMS.txt`:

```bash
shasum -a 256 -c SHA256SUMS.txt   # macOS
sha256sum -c SHA256SUMS.txt       # Linux
```

## Cómo ejecutarlo (macOS Apple Silicon)

```bash
unzip storysmith-darwin-arm64.zip
chmod +x storysmith-darwin-arm64
# Si Gatekeeper lo bloquea ("descargado de internet"):
xattr -d com.apple.quarantine storysmith-darwin-arm64
./storysmith-darwin-arm64
```

Luego abre **http://localhost:48090** en el navegador.

Opciones:

```text
-port string        dirección de escucha (por defecto ":48090")
-data string        directorio de datos (por defecto, junto al ejecutable)
-no-browser         no abrir una ventana del navegador al iniciar
```

## Requisitos para escribir con IA

Necesitas **Ollama** corriendo en local:

```bash
ollama serve                      # normalmente ya corre como servicio
ollama pull llama3.1:8b           # o qwen2.5:14b, mistral, etc.
```

En la pestaña **Configuración API** deja la base URL en
`http://localhost:11434/v1`, pulsa *Probar* y crea tu proyecto en
**Parámetros de Novela**.

> Nota: las pestañas Outline y Escritura muestran la interfaz base;
> el flujo completo de generación contra Ollama se está integrando.
