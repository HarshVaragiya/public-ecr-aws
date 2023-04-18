pipeline {
    agent any
    stages {
        stage("build"){
            steps {
                // rupasa k naap p chal ja please
                sh 'docker build -t public-ecr-aws:latest .'
            }
        }
        stage("exec") {
            steps{
                withCredentials([string(credentialsId: 'gotify-url', variable: 'GOTIFY_URL')]) {
                    withCredentials([usernamePassword(credentialsId: 'redis-connection-string', passwordVariable: 'REDIS_PASSWORD', usernameVariable: 'REDIS_HOST')]) {
                        sh '''docker run --rm \
                        -e REDIS_HOST=${REDIS_HOST} \
                        -e REDIS_PASSWORD=${REDIS_PASSWORD} \
                        -v $PWD:/pwd \
                        public-ecr-aws:latest ${ARGS}
                        '''
                    }
                }
            }
        }
        stage("cleanup"){
            steps {
                sh 'docker image rm public-ecr-aws:latest'
                echo 'cleaning up'
                sh 'rm -rf * && rm -rf .git*'
                sh 'ls -alh'
            }
        }
    }
}