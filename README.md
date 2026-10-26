# EntregaViva

Painel de operação para acompanhar pedidos entre restaurante, cozinha e entregadores. O fluxo de estados é validado no servidor e as telas abertas recebem as mudanças por Server-Sent Events.

## Rodar localmente

Requer Node.js 20 ou superior.

```bash
npm install
npm start
```

Abra <http://localhost:3003>. Pedidos fictícios demonstram cada etapa; o banco fica em `data/orders.sqlite`.

## Funcionalidades

- Quadro de pedidos por etapa: recebido, preparando, pronto e em rota.
- Criação e cancelamento de pedidos.
- Transições de estado permitidas e histórico de eventos.
- Atualizações em tempo real do painel por Server-Sent Events.
- Separação das listas ativas e encerradas.

## Decisões técnicas

- O servidor é a autoridade do fluxo. Cada mudança valida a etapa atual e rejeita transições inválidas.
- A atualização usa `WHERE status = estado_anterior` para evitar que duas sessões sobrescrevam uma à outra sem perceber.
- Server-Sent Events bastam para atualizações do servidor para os painéis; as ações do usuário usam HTTP comum.
- Os dados de exemplo são fictícios. A aplicação não realiza pagamentos nem envia mensagens aos clientes.

## API

- `GET /api/orders` — pedidos e histórico.
- `POST /api/orders` — cria pedido.
- `PATCH /api/orders/:id/status` — muda uma etapa válida.
- `GET /api/events` — fluxo de eventos SSE.

## Próximas melhorias

Login por perfil, catálogo de itens, estimativa de tempo, mapa de entregadores e comunicação com o cliente.
