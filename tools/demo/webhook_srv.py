import http.server,sys
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(s):
        s.rfile.read(int(s.headers.get('Content-Length',0))); s.send_response(200); s.end_headers()
    def log_message(*a): pass
http.server.HTTPServer(('127.0.0.1',18099),H).serve_forever()
