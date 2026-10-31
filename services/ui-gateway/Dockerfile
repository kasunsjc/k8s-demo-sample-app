FROM node:22-alpine
ENV NODE_ENV=production
WORKDIR /app
COPY package.json server.js tracing.js ./
COPY public ./public
USER node
EXPOSE 3000
CMD ["node", "server.js"]
